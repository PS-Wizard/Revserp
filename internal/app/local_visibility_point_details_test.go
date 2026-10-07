package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

func pointDetailsSnapshot() localvisibility.LocalRunSnapshot {
	points := make([]localvisibility.GridPoint, 0, localvisibility.GridPointCount)
	for i := 0; i < localvisibility.GridPointCount; i++ {
		points = append(points, localvisibility.GridPoint{PointIndex: i})
	}
	return localvisibility.LocalRunSnapshot{
		Queries:       []string{"coffee", "espresso"},
		TargetPlaceID: "target-place",
		Points:        points,
	}
}

func pointDetailsRow(queryIndex int16, callStatus, matchStatus string) sqlc.GetLocalVisibilityPointResultsRow {
	return sqlc.GetLocalVisibilityPointResultsRow{
		QueryIndex:  queryIndex,
		CallStatus:  callStatus,
		MatchStatus: matchStatus,
	}
}

func TestBuildPointDetailsAbsentRowsStayPending(t *testing.T) {
	response := buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), nil)
	if response.RunID != "run-1" || response.PointIndex != 4 || response.TargetPlaceID != "target-place" {
		t.Fatalf("response identity = %#v", response)
	}
	if len(response.Queries) != 2 {
		t.Fatalf("queries = %d, want 2 frozen queries", len(response.Queries))
	}
	for i, query := range response.Queries {
		if query.QueryIndex != i || query.Query != pointDetailsSnapshot().Queries[i] {
			t.Fatalf("query %d = %#v", i, query)
		}
		if query.CallStatus != "pending" || query.MatchStatus != "unknown" {
			t.Fatalf("absent query %d status = %s/%s, want pending/unknown", i, query.CallStatus, query.MatchStatus)
		}
		if query.Rank != nil || query.Error != nil {
			t.Fatalf("absent query %d rank/error must be null: %#v", i, query)
		}
		if query.Places == nil || len(query.Places) != 0 {
			t.Fatalf("absent query %d places must be an empty array", i)
		}
	}
}

func TestBuildPointDetailsSuccessNonemptyMarksTargetOnly(t *testing.T) {
	raw := `{"places":[
		{"position":3,"title":"Other","address":"A St","placeId":"other","rating":4.0,"ratingCount":5},
		{"position":1,"title":"Target","address":"B St","placeId":"target-place","rating":4.5,"ratingCount":10},
		{"title":"No ID","address":"C St"}
	]}`
	row := pointDetailsRow(0, "success_nonempty", "found")
	row.RawResponse = []byte(raw)
	row.Rank = pgtype.Int4{Int32: 1, Valid: true}
	response := buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), []sqlc.GetLocalVisibilityPointResultsRow{row})
	if len(response.Queries) != 2 {
		t.Fatalf("queries = %d, want every frozen query", len(response.Queries))
	}
	got := response.Queries[0]
	if got.CallStatus != "success_nonempty" || got.MatchStatus != "found" {
		t.Fatalf("status = %s/%s", got.CallStatus, got.MatchStatus)
	}
	if got.Rank == nil || *got.Rank != 1 || got.Error != nil {
		t.Fatalf("rank/error = %#v/%#v", got.Rank, got.Error)
	}
	if len(got.Places) != 3 {
		t.Fatalf("places = %d, want provider order preserved", len(got.Places))
	}
	if got.Places[0].Position == nil || *got.Places[0].Position != 3 {
		t.Fatalf("first position = %#v, want verbatim 3", got.Places[0].Position)
	}
	if got.Places[0].IsTarget || !got.Places[1].IsTarget || got.Places[2].IsTarget {
		t.Fatalf("is_target flags = %v %v %v", got.Places[0].IsTarget, got.Places[1].IsTarget, got.Places[2].IsTarget)
	}
	if got.Places[2].PlaceID != nil {
		t.Fatalf("place without id must stay null, got %q", *got.Places[2].PlaceID)
	}
	if got.Places[0].Rating == nil || *got.Places[0].Rating != 4.0 {
		t.Fatalf("rating = %#v", got.Places[0].Rating)
	}
	if got.Places[2].Position != nil || got.Places[2].Rating != nil || got.Places[2].RatingCount != nil {
		t.Fatalf("missing numerics must stay null: %#v", got.Places[2])
	}
	pending := response.Queries[1]
	if pending.CallStatus != "pending" || len(pending.Places) != 0 {
		t.Fatalf("unrecorded query = %#v, want pending with empty places", pending)
	}
}

func TestBuildPointDetailsRequestFailedHidesPlaces(t *testing.T) {
	raw := `{"places":[{"position":1,"title":"Target","address":"B St","placeId":"target-place"}]}`
	row := pointDetailsRow(1, "request_failed", "unknown")
	row.RawResponse = []byte(raw)
	row.Error = pgtype.Text{String: "viewport drifted", Valid: true}
	response := buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), []sqlc.GetLocalVisibilityPointResultsRow{row})
	got := response.Queries[1]
	if len(got.Places) != 0 {
		t.Fatalf("request_failed places = %d, want none decoded", len(got.Places))
	}
	if got.Error == nil || *got.Error != "viewport drifted" {
		t.Fatalf("error = %#v, want stored message", got.Error)
	}
	if got.Rank != nil {
		t.Fatalf("rank = %#v, want null", got.Rank)
	}
}

func TestBuildPointDetailsRequestFailedFallbackError(t *testing.T) {
	row := pointDetailsRow(0, "request_failed", "unknown")
	response := buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), []sqlc.GetLocalVisibilityPointResultsRow{row})
	if response.Queries[0].Error == nil || *response.Queries[0].Error == "" {
		t.Fatalf("error = %#v, want fallback text", response.Queries[0].Error)
	}
}

func TestBuildPointDetailsSuccessEmptyStaysExplicit(t *testing.T) {
	row := pointDetailsRow(0, "success_empty", "absent")
	response := buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), []sqlc.GetLocalVisibilityPointResultsRow{row})
	got := response.Queries[0]
	if got.CallStatus != "success_empty" || got.Error != nil || len(got.Places) != 0 {
		t.Fatalf("success_empty = %#v", got)
	}
}

func TestBuildPointDetailsMalformedRawSurfacesError(t *testing.T) {
	cases := map[string][]byte{
		"missing":  nil,
		"broken":   []byte(`{"places":`),
		"no array": []byte(`{"ll":"@1,2,14z"}`),
		"empty":    []byte(`{"places":[]}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			row := pointDetailsRow(0, "success_nonempty", "absent")
			row.RawResponse = raw
			response := buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), []sqlc.GetLocalVisibilityPointResultsRow{row})
			got := response.Queries[0]
			if got.Error == nil || *got.Error == "" {
				t.Fatalf("error = %#v, want surfaced decode failure", got.Error)
			}
			if len(got.Places) != 0 {
				t.Fatalf("places = %d, want empty with error", len(got.Places))
			}
			if got.CallStatus != "success_nonempty" {
				t.Fatalf("call status = %s, want stored value kept", got.CallStatus)
			}
		})
	}
}

func TestBuildPointDetailsContractShape(t *testing.T) {
	raw := `{"places":[{"position":1,"title":"T","address":"A","placeId":"target-place","rating":4.5,"ratingCount":7}]}`
	row := pointDetailsRow(0, "success_nonempty", "found")
	row.RawResponse = []byte(raw)
	row.Rank = pgtype.Int4{Int32: 1, Valid: true}
	encoded, err := json.Marshal(buildLocalVisibilityPointDetails("run-1", 4, pointDetailsSnapshot(), []sqlc.GetLocalVisibilityPointResultsRow{row}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"run_id", "point_index", "target_place_id", "queries"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing top-level %q in %s", key, encoded)
		}
	}
	query := decoded["queries"].([]any)[0].(map[string]any)
	for _, key := range []string{"query_index", "query", "call_status", "match_status", "rank", "error", "places"} {
		if _, ok := query[key]; !ok {
			t.Fatalf("missing query %q in %s", key, encoded)
		}
	}
	place := query["places"].([]any)[0].(map[string]any)
	for _, key := range []string{"position", "title", "address", "place_id", "rating", "rating_count", "is_target"} {
		if _, ok := place[key]; !ok {
			t.Fatalf("missing place %q in %s", key, encoded)
		}
	}
}

func callGetLocalVisibilityRunPoint(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, runID, pointIndex string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{
		"projectID":  projectID,
		"locationID": locationID,
		"runID":      runID,
		"pointIndex": pointIndex,
	}, "")
	rr := httptest.NewRecorder()
	app.handleGetLocalVisibilityRunPoint(rr, req)
	return rr
}

func TestLocalVisibilityPointDetailsEndpoint(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "pointdetail")
	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"expected_credits":135}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityRunCreatedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created run: %v", err)
	}
	rr = callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "4")
	if rr.Code != http.StatusOK {
		t.Fatalf("point status = %d body=%s", rr.Code, rr.Body.String())
	}
	var fresh localVisibilityPointDetailsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &fresh); err != nil {
		t.Fatalf("decode point details: %v body=%s", err, rr.Body.String())
	}
	if fresh.RunID != created.ID || fresh.PointIndex != 4 || fresh.TargetPlaceID != "place-pointdetail" {
		t.Fatalf("point identity = %#v", fresh)
	}
	if len(fresh.Queries) != 5 || fresh.Queries[0].Query != "a" || fresh.Queries[4].Query != "e" {
		t.Fatalf("frozen queries = %#v", fresh.Queries)
	}
	for _, query := range fresh.Queries {
		if query.CallStatus != "pending" || len(query.Places) != 0 {
			t.Fatalf("fresh query = %#v, want pending", query)
		}
	}
	target := "place-pointdetail"
	nonemptyRaw := fmt.Sprintf(`{"places":[{"position":2,"title":"Target Shop","address":"1 Main St","placeId":%q,"rating":4.5,"ratingCount":12},{"position":1,"title":"Rival","address":"2 Main St","placeId":"other-place","rating":4.0,"ratingCount":3}]}`, target)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known,raw_response,error)
		VALUES ($1,0,4,'success_nonempty','found',2,3,TRUE,$2::jsonb,NULL)`, created.ID, nonemptyRaw); err != nil {
		t.Fatalf("insert nonempty result: %v", err)
	}
	failedRaw := fmt.Sprintf(`{"places":[{"position":1,"title":"Stale","address":"X","placeId":%q}]}`, target)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,credits,credit_known,raw_response,error)
		VALUES ($1,1,4,'request_failed','unknown',0,FALSE,$2::jsonb,'viewport drifted')`, created.ID, failedRaw); err != nil {
		t.Fatalf("insert failed result: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,credits,credit_known)
		VALUES ($1,2,4,'success_empty','absent',3,TRUE)`, created.ID); err != nil {
		t.Fatalf("insert empty result: %v", err)
	}
	rr = callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "4")
	if rr.Code != http.StatusOK {
		t.Fatalf("point status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response localVisibilityPointDetailsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode point details: %v body=%s", err, rr.Body.String())
	}
	found := response.Queries[0]
	if found.CallStatus != "success_nonempty" || found.Rank == nil || *found.Rank != 2 || found.Error != nil {
		t.Fatalf("nonempty query = %#v", found)
	}
	if len(found.Places) != 2 || found.Places[0].Position == nil || *found.Places[0].Position != 2 {
		t.Fatalf("places order/positions = %#v", found.Places)
	}
	if found.Places[0].PlaceID == nil || !found.Places[0].IsTarget || found.Places[1].IsTarget {
		t.Fatalf("target flags = %#v", found.Places)
	}
	failed := response.Queries[1]
	if failed.CallStatus != "request_failed" || failed.Error == nil || *failed.Error != "viewport drifted" || len(failed.Places) != 0 {
		t.Fatalf("failed query = %#v", failed)
	}
	empty := response.Queries[2]
	if empty.CallStatus != "success_empty" || empty.Error != nil || len(empty.Places) != 0 {
		t.Fatalf("empty query = %#v", empty)
	}
	if response.Queries[3].CallStatus != "pending" || response.Queries[4].CallStatus != "pending" {
		t.Fatalf("unrecorded queries = %#v", response.Queries[3:])
	}
	if rr := callUpdateLocationQueries(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, layer4DraftJSON("v", "w", "x", "y", "z")); rr.Code != http.StatusOK {
		t.Fatalf("update queries status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "4")
	var refrozen localVisibilityPointDetailsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &refrozen); err != nil {
		t.Fatalf("decode refrozen point: %v", err)
	}
	if refrozen.TargetPlaceID != target || refrozen.Queries[0].Query != "a" {
		t.Fatalf("snapshot drifted: %#v", refrozen)
	}
}

func TestLocalVisibilityPointDetailsValidationAndSecurity(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "pointguard")
	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"expected_credits":135}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityRunCreatedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, pointIndex := range []string{"9", "-1", "abc", ""} {
		if rr := callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, pointIndex); rr.Code != http.StatusBadRequest {
			t.Errorf("point %q status = %d, want 400; body=%s", pointIndex, rr.Code, rr.Body.String())
		}
	}
	unknownRun := "00000000-0000-0000-0000-0000000000ff"
	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"unknown run": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, unknownRun, "4")
		},
		"outsider": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPoint(t, fx.app, fx.outsider, fx.projectID.String(), location.ID, created.ID, "4")
		},
		"invalid run id": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "not-a-uuid", "4")
		},
	} {
		t.Run(name, func(t *testing.T) {
			rr := call()
			want := http.StatusNotFound
			if strings.Contains(name, "invalid") {
				want = http.StatusBadRequest
			}
			if rr.Code != want {
				t.Errorf("status = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
			}
		})
	}
	var secondProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'point-guard-other','https://point-guard-other.example') RETURNING id`, fx.orgID).Scan(&secondProject); err != nil {
		t.Fatalf("create second project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(fx.ctx, `DELETE FROM projects WHERE id=$1`, secondProject) })
	if rr := callGetLocalVisibilityRunPoint(t, fx.app, fx.ownerID, secondProject.String(), location.ID, created.ID, "4"); rr.Code != http.StatusNotFound {
		t.Errorf("wrong project status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
