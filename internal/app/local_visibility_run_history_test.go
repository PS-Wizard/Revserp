package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Run-history tests. Pagination and item projection run everywhere; handler
// tests use the isolated fixture database and skip when it is absent. The list
// never writes and never calls a provider.

func TestParseRunHistoryPagination(t *testing.T) {
	for _, tc := range []struct {
		name, query           string
		wantLimit, wantOffset int
		wantErr               bool
	}{
		{"defaults", "", 20, 0, false},
		{"explicit window", "limit=5&offset=10", 5, 10, false},
		{"max limit", "limit=100", 100, 0, false},
		{"zero limit fails", "limit=0", 0, 0, true},
		{"over max fails", "limit=101", 0, 0, true},
		{"non-numeric limit fails", "limit=many", 0, 0, true},
		{"negative limit fails", "limit=-5", 0, 0, true},
		{"negative offset fails", "offset=-1", 0, 0, true},
		{"non-numeric offset fails", "offset=far", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("parse test query: %v", err)
			}
			limit, offset, err := parseRunHistoryPagination(values)
			if tc.wantErr != (err != nil) {
				t.Fatalf("pagination %q err = %v, wantErr %v", tc.query, err, tc.wantErr)
			}
			if !tc.wantErr && (limit != tc.wantLimit || offset != tc.wantOffset) {
				t.Fatalf("pagination %q = %d/%d, want %d/%d", tc.query, limit, offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

func runHistoryTestRow(status, snapshot string) localVisibilityRunHistoryRow {
	created := pgtype.Timestamptz{Time: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Valid: true}
	completed := pgtype.Timestamptz{Time: time.Date(2026, 10, 1, 12, 5, 0, 0, time.UTC), Valid: true}
	if status != "completed" {
		completed.Valid = false
	}
	row := localVisibilityRunHistoryRow{
		Status: status, RadiusM: 5000, ExpectedCredits: 135, CreditsUsed: 120,
		Snapshot: []byte(snapshot), CreatedAt: created, CompletedAt: completed,
	}
	_ = row.ID.Scan("00000000-0000-0000-0000-000000000011")
	return row
}

func TestNewLocalVisibilityRunHistoryItem(t *testing.T) {
	item := newLocalVisibilityRunHistoryItem(runHistoryTestRow("completed",
		`{"queries":["a","b","c"],"points":[{},{},{},{}]}`))
	if item.Status != "completed" || item.RadiusM != 5000 || item.ExpectedCredits != 135 || item.CreditsUsed != 120 {
		t.Fatalf("run metadata = %#v", item)
	}
	if item.QueryCount != 3 || item.GridPointCount != 4 || item.TotalCells != 12 {
		t.Fatalf("frozen grid shape = %d/%d/%d, want 3/4/12", item.QueryCount, item.GridPointCount, item.TotalCells)
	}
	if item.CreatedAt != "2026-10-01T12:00:00Z" || item.CompletedAt == nil || *item.CompletedAt != "2026-10-01T12:05:00Z" {
		t.Fatalf("timestamps = %q/%v", item.CreatedAt, item.CompletedAt)
	}
	if item.Error != nil {
		t.Fatalf("error = %v, want null without a stored error", *item.Error)
	}

	running := newLocalVisibilityRunHistoryItem(runHistoryTestRow("running", `{"queries":["a"],"points":[{}]}`))
	if running.CompletedAt != nil {
		t.Fatalf("running completed_at = %v, want null", *running.CompletedAt)
	}

	corrupt := newLocalVisibilityRunHistoryItem(runHistoryTestRow("failed", `{"queries":`))
	if corrupt.QueryCount != 0 || corrupt.GridPointCount != 0 || corrupt.TotalCells != 0 {
		t.Fatalf("corrupt snapshot = %d/%d/%d, want zeros that keep the list readable",
			corrupt.QueryCount, corrupt.GridPointCount, corrupt.TotalCells)
	}
	if corrupt.ID == "" || corrupt.Status != "failed" {
		t.Fatalf("corrupt row lost its identity: %#v", corrupt)
	}
}

func TestLocationResponseWithRadiusM(t *testing.T) {
	response := locationResponseWithRadiusM(localVisibilityLocationResponse{ID: "loc"}, 5000)
	if response.RadiusM != 5000 || response.ID != "loc" {
		t.Fatalf("radius converter = %#v", response)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal location: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode location: %v", err)
	}
	if decoded["radius_m"] != float64(5000) {
		t.Fatalf("radius_m missing from location JSON: %v", decoded)
	}

	raw, err = json.Marshal(localVisibilityLocationResponse{})
	if err != nil {
		t.Fatalf("marshal unwired location: %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode unwired location: %v", err)
	}
	if _, present := decoded["radius_m"]; present {
		t.Fatalf("unwired read paths must keep their exact shape, got %v", decoded)
	}
}

func callListLocalVisibilityRuns(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	req.URL.RawQuery = query
	rr := httptest.NewRecorder()
	app.handleListLocalVisibilityRuns(rr, req)
	return rr
}

func insertRunHistoryRow(t *testing.T, fx localVisibilityFixture, locationID, status string, radius int, snapshot string) {
	t.Helper()
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_runs(location_id, status, radius_m, snapshot, expected_credits, reserved_credits)
		VALUES ($1,$2,$3,$4::jsonb,135,135)`, locationID, status, radius, snapshot); err != nil {
		t.Fatalf("insert stored run: %v", err)
	}
}

func TestListLocalVisibilityRunHistory(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "history")
	insertRunHistoryRow(t, fx, location.ID, "completed", 5000, `{"queries":["a","b"],"points":[{},{},{}]}`)
	insertRunHistoryRow(t, fx, location.ID, "failed", 3000, `{"queries":["a"],"points":[{}]}`)

	rr := callListLocalVisibilityRuns(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response localVisibilityRunHistoryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode run list: %v body=%s", err, rr.Body.String())
	}
	if response.Total != 2 || len(response.Runs) != 2 {
		t.Fatalf("run list = %#v, want 2 newest-first", response)
	}
	first := response.Runs[0]
	if first.Status != "failed" || first.RadiusM != 3000 || first.QueryCount != 1 || first.GridPointCount != 1 || first.TotalCells != 1 {
		t.Fatalf("newest run = %#v, want the failed 3000m row first", first)
	}
	second := response.Runs[1]
	if second.Status != "completed" || second.QueryCount != 2 || second.GridPointCount != 3 || second.TotalCells != 6 {
		t.Fatalf("older run = %#v, want frozen 2x3 metadata", second)
	}
	if second.CompletedAt == nil {
		t.Fatal("completed run must carry completed_at")
	}

	rr = callListLocalVisibilityRuns(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "limit=1&offset=1")
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || response.Total != 2 || len(response.Runs) != 1 {
		t.Fatalf("paged list = %s, %v; want total 2 with one row", rr.Body.String(), err)
	}
	if response.Runs[0].Status != "completed" {
		t.Fatalf("paged row = %#v, want the older run", response.Runs[0])
	}

	if rr := callListLocalVisibilityRuns(t, fx.app, fx.memberID, fx.projectID.String(), location.ID, ""); rr.Code != http.StatusOK {
		t.Fatalf("member list status = %d, want 200", rr.Code)
	}
	if rr := callListLocalVisibilityRuns(t, fx.app, fx.outsider, fx.projectID.String(), location.ID, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider list status = %d, want 404", rr.Code)
	}
	if rr := callListLocalVisibilityRuns(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "limit=500"); rr.Code != http.StatusBadRequest {
		t.Fatalf("over-max limit status = %d, want 400", rr.Code)
	}
	if rr := callListLocalVisibilityRuns(t, fx.app, fx.ownerID, fx.projectID.String(), "00000000-0000-0000-0000-0000000000ff", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown location status = %d, want 404", rr.Code)
	}
}
