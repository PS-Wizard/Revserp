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

func competitorsSnapshot() localvisibility.LocalRunSnapshot {
	return localvisibility.LocalRunSnapshot{
		Queries:       []string{"coffee", "espresso"},
		TargetPlaceID: "target-place",
		Points: []localvisibility.GridPoint{
			{PointIndex: 0},
			{PointIndex: 1},
			{PointIndex: 2},
		},
	}
}

func competitorsRow(pointIndex, queryIndex int, callStatus, raw string) sqlc.GetLocalVisibilityRunCompetitorResultsRow {
	return sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		PointIndex:  int16(pointIndex),
		QueryIndex:  int16(queryIndex),
		CallStatus:  callStatus,
		RawResponse: []byte(raw),
	}
}

func findCompetitor(t *testing.T, response localVisibilityCompetitorsResponse, placeID string) localVisibilityCompetitorResponse {
	t.Helper()
	for _, competitor := range response.Competitors {
		if competitor.PlaceID == placeID {
			return competitor
		}
	}
	t.Fatalf("competitor %q missing from %#v", placeID, response.Competitors)
	return localVisibilityCompetitorResponse{}
}

func TestBuildCompetitorsIdenticalNamesDifferentIDsAndDuplicates(t *testing.T) {
	raw := `{"places":[
		{"position":8,"title":"Shared Name","address":"A St","placeId":"id-1"},
		{"position":2,"title":"Shared Name","address":"A St","placeId":"id-1"},
		{"position":1,"title":"Shared Name","address":"A St","placeId":"id-2"},
		{"title":"Shared Name","address":"A St"}
	]}`
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 2 {
		t.Fatalf("competitors = %#v, want two by distinct id despite identical names", response.Competitors)
	}
	first := findCompetitor(t, response, "id-1")
	if first.Title != "Shared Name" || first.QueryPointsSeen != 1 || first.BestRank == nil || *first.BestRank != 2 {
		t.Fatalf("id-1 = %#v, want duplicate inside one list counted once", first)
	}
	if len(first.QueryIndexes) != 1 || first.QueryIndexes[0] != 0 {
		t.Fatalf("id-1 query indexes = %#v", first.QueryIndexes)
	}
	if response.IdlessEntries != 1 {
		t.Fatalf("idless_entries = %d, want 1", response.IdlessEntries)
	}
}

func TestBuildCompetitorsExcludesFrozenTargetOnly(t *testing.T) {
	raw := `{"places":[
		{"position":1,"title":"Own Store","address":"1 Main","placeId":"target-place"},
		{"position":2,"title":"Own Store","address":"1 Main","placeId":"other"}
	]}`
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 1 || response.Competitors[0].PlaceID != "other" {
		t.Fatalf("competitors = %#v, want only non-target id even with matching name", response.Competitors)
	}
	if response.IdlessEntries != 0 {
		t.Fatalf("idless_entries = %d, want target excluded not idless", response.IdlessEntries)
	}
}

func TestBuildCompetitorsCoverageAndFrequencyDenominator(t *testing.T) {
	rawA := `{"places":[{"position":1,"title":"A","address":"A St","placeId":"id-a"}]}`
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", rawA),
		competitorsRow(1, 0, "success_nonempty", rawA),
		competitorsRow(2, 0, "request_failed", ""),
		competitorsRow(0, 1, "success_empty", ""),
		competitorsRow(2, 1, "success_nonempty", `{"places":`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.TotalQueryPoints != 6 {
		t.Fatalf("total = %d, want len(points)*len(queries)=6", response.TotalQueryPoints)
	}
	if response.ContributingQueryPoints != 3 {
		t.Fatalf("contributing = %d, want 3 (2 nonempty + 1 empty)", response.ContributingQueryPoints)
	}
	if response.FailedQueryPoints != 1 || response.PendingQueryPoints != 1 || response.UnreadableQueryPoints != 1 {
		t.Fatalf("failed/pending/unreadable = %d/%d/%d", response.FailedQueryPoints, response.PendingQueryPoints, response.UnreadableQueryPoints)
	}
	if len(response.Competitors) != 1 {
		t.Fatalf("competitors = %#v", response.Competitors)
	}
	got := response.Competitors[0]
	if got.QueryPointsSeen != 2 {
		t.Fatalf("query_points_seen = %d, want 2 distinct cells", got.QueryPointsSeen)
	}
	if len(got.QueryIndexes) != 1 || got.QueryIndexes[0] != 0 {
		t.Fatalf("query_indexes = %#v, want [0]", got.QueryIndexes)
	}
}

func TestBuildCompetitorsRanksNullForMissingOrNonpositivePositions(t *testing.T) {
	ranked := `{"places":[{"position":3,"title":"Ranked","address":"R","placeId":"id-r"}]}`
	nonpositive := `{"places":[{"position":0,"title":"Ranked","address":"R","placeId":"id-r"},{"title":"NoPos","address":"N","placeId":"id-n"}]}`
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", ranked),
		competitorsRow(1, 0, "success_nonempty", nonpositive),
	})
	if err != nil {
		t.Fatal(err)
	}
	rankedEntry := findCompetitor(t, response, "id-r")
	if rankedEntry.BestRank == nil || *rankedEntry.BestRank != 3 {
		t.Fatalf("best_rank = %#v, want min positive 3", rankedEntry.BestRank)
	}
	noPos := findCompetitor(t, response, "id-n")
	if noPos.BestRank != nil {
		t.Fatalf("best_rank = %#v, want null when no positive position", noPos.BestRank)
	}
	if noPos.QueryPointsSeen != 1 {
		t.Fatalf("query_points_seen = %d", noPos.QueryPointsSeen)
	}
}

func TestBuildCompetitorsDeterministicSort(t *testing.T) {
	rawFrequent := `{"places":[{"position":2,"title":"Freq3","address":"x","placeId":"id-c"}]}`
	rawTied := `{"places":[
		{"position":5,"title":"Rank5","address":"x","placeId":"id-b"},
		{"position":2,"title":"Rank2","address":"x","placeId":"id-a"},
		{"title":"NullRank","address":"x","placeId":"id-e"},
		{"position":2,"title":"Rank2Tie","address":"x","placeId":"id-d"}
	]}`
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", rawTied),
		competitorsRow(0, 1, "success_nonempty", rawTied),
		competitorsRow(1, 0, "success_nonempty", rawFrequent),
		competitorsRow(1, 1, "success_nonempty", rawFrequent),
		competitorsRow(2, 0, "success_nonempty", rawFrequent),
	})
	if err != nil {
		t.Fatal(err)
	}
	order := make([]string, 0, len(response.Competitors))
	for _, competitor := range response.Competitors {
		order = append(order, competitor.PlaceID)
	}
	want := []string{"id-c", "id-a", "id-d", "id-b", "id-e"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want frequency desc then rank asc null last then id asc", order)
	}
}

func TestBuildCompetitorsRefusesMissingFrozenTarget(t *testing.T) {
	snapshot := competitorsSnapshot()
	snapshot.TargetPlaceID = ""
	if _, err := buildLocalVisibilityCompetitors("run-1", snapshot, nil); err == nil {
		t.Fatal("want error when frozen target place id is missing")
	}
}

func TestBuildCompetitorsContractShape(t *testing.T) {
	raw := `{"places":[{"position":1,"title":"T","address":"A","placeId":"id-a"}]}`
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"run_id", "point_index", "target_place_id", "queries", "total_query_points", "contributing_query_points", "failed_query_points", "pending_query_points", "unreadable_query_points", "idless_entries", "competitors"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing coverage key %q in %s", key, encoded)
		}
	}
	competitor := decoded["competitors"].([]any)[0].(map[string]any)
	for _, key := range []string{"place_id", "title", "address", "query_points_seen", "best_rank", "query_indexes"} {
		if _, ok := competitor[key]; !ok {
			t.Fatalf("missing competitor key %q in %s", key, encoded)
		}
	}
}

func callGetLocalVisibilityRunCompetitors(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, runID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{
		"projectID":  projectID,
		"locationID": locationID,
		"runID":      runID,
	}, "")
	rr := httptest.NewRecorder()
	app.handleGetLocalVisibilityRunCompetitors(rr, req)
	return rr
}

func TestLocalVisibilityCompetitorsEndpointAndSecurity(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "competitors")
	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"expected_credits":135}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityRunCreatedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created run: %v", err)
	}
	target := "place-competitors"
	raw := `{"places":[{"position":1,"title":"Target Shop","address":"1 Main St","placeId":"` + target + `"},{"position":2,"title":"Rival","address":"2 Main St","placeId":"rival-1"}]}`
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known,raw_response,error)
		VALUES ($1,0,0,'success_nonempty','found',2,3,TRUE,$2::jsonb,NULL)`, created.ID, raw); err != nil {
		t.Fatalf("insert result: %v", err)
	}
	rr = callGetLocalVisibilityRunCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("competitors status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response localVisibilityCompetitorsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode competitors: %v body=%s", err, rr.Body.String())
	}
	if response.RunID != created.ID || response.TargetPlaceID != target {
		t.Fatalf("identity = %#v", response)
	}
	if response.TotalQueryPoints != localvisibility.GridPointCount*len(response.Queries) {
		t.Fatalf("total = %d, want grid*queries", response.TotalQueryPoints)
	}
	if len(response.Competitors) != 1 || response.Competitors[0].PlaceID != "rival-1" {
		t.Fatalf("competitors = %#v, want only the rival", response.Competitors)
	}
	if response.ContributingQueryPoints != 1 {
		t.Fatalf("contributing = %d, want 1", response.ContributingQueryPoints)
	}

	unknownRun := "00000000-0000-0000-0000-0000000000ff"
	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"unknown run": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, unknownRun)
		},
		"outsider": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunCompetitors(t, fx.app, fx.outsider, fx.projectID.String(), location.ID, created.ID)
		},
		"invalid run id": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "not-a-uuid")
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
}

func competitorsPlacesJSON(places ...string) string {
	return `{"places":[` + strings.Join(places, ",") + `]}`
}

func competitorsPlaceJSON(position int, placeID, title, website string) string {
	place := fmt.Sprintf(`{"position":%d,"title":%q,"address":"A St","placeId":%q`, position, title, placeID)
	if website != "" {
		place += fmt.Sprintf(`,"website":%q`, website)
	}
	return place + "}"
}

func TestBuildCompetitorsSameBrandDomainSharedHost(t *testing.T) {
	raw := competitorsPlacesJSON(
		competitorsPlaceJSON(1, "target-place", "Own Shop", "https://Example.com/"),
		competitorsPlaceJSON(2, "id-same", "Rival", "http://example.com"),
		competitorsPlaceJSON(3, "id-other", "Other", "https://other.example"),
	)
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 2 {
		t.Fatalf("competitors = %#v, want target excluded and both rivals retained", response.Competitors)
	}
	if !findCompetitor(t, response, "id-same").SameBrandDomain {
		t.Fatalf("id-same should match shared caseless host")
	}
	if findCompetitor(t, response, "id-other").SameBrandDomain {
		t.Fatalf("id-other host must not match")
	}
}

func TestBuildCompetitorsSameBrandDomainMissingOrInvalidWebsites(t *testing.T) {
	t.Run("missing target host", func(t *testing.T) {
		raw := competitorsPlacesJSON(
			`{"position":1,"title":"Own","address":"A St","placeId":"target-place","website":"not a url"}`,
			competitorsPlaceJSON(2, "id-a", "Rival", "https://example.com"),
		)
		response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
			competitorsRow(0, 0, "success_nonempty", raw),
		})
		if err != nil {
			t.Fatal(err)
		}
		if findCompetitor(t, response, "id-a").SameBrandDomain {
			t.Fatalf("missing target host must yield false")
		}
	})
	t.Run("invalid competitor websites keep evidence", func(t *testing.T) {
		raw := competitorsPlacesJSON(
			competitorsPlaceJSON(1, "target-place", "Own", "https://example.com"),
			`{"position":2,"title":"NoSite","address":"A St","placeId":"id-a"}`,
			`{"position":3,"title":"BadType","address":"A St","placeId":"id-b","website":123}`,
			competitorsPlaceJSON(4, "id-c", "Ftp", "ftp://example.com"),
			competitorsPlaceJSON(5, "id-d", "Good", "https://example.com"),
		)
		response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
			competitorsRow(0, 0, "success_nonempty", raw),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, placeID := range []string{"id-a", "id-b", "id-c"} {
			if findCompetitor(t, response, placeID).SameBrandDomain {
				t.Fatalf("%s must not match", placeID)
			}
		}
		if !findCompetitor(t, response, "id-d").SameBrandDomain {
			t.Fatalf("valid shared host must match")
		}
		badType := findCompetitor(t, response, "id-b")
		if badType.QueryPointsSeen != 1 || badType.BestRank == nil || *badType.BestRank != 3 {
			t.Fatalf("invalid website type must not invalidate place evidence: %#v", badType)
		}
	})
}

func TestBuildCompetitorsSameBrandDomainNameMismatchAndOrderIndependent(t *testing.T) {
	rawEarly := competitorsPlacesJSON(
		competitorsPlaceJSON(1, "id-rival", "Joe Coffee", "https://rival.example"),
		competitorsPlaceJSON(2, "id-same", "Same Brand", "https://Shared.example"),
	)
	rawLate := competitorsPlacesJSON(competitorsPlaceJSON(1, "target-place", "Joe Coffee", "https://shared.example."))
	response, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", rawEarly),
		competitorsRow(2, 1, "success_nonempty", rawLate),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 2 {
		t.Fatalf("competitors = %#v, want target excluded", response.Competitors)
	}
	if findCompetitor(t, response, "id-rival").SameBrandDomain {
		t.Fatalf("same company name with unrelated host must not match")
	}
	same := findCompetitor(t, response, "id-same")
	if !same.SameBrandDomain {
		t.Fatalf("shared host seen before target must match, order independent")
	}
	if same.QueryPointsSeen != 1 || same.BestRank == nil || *same.BestRank != 2 {
		t.Fatalf("grouping/rank/frequency must be unchanged: %#v", same)
	}
}

func TestBuildPointCompetitorsScopeCountsAndNoLeak(t *testing.T) {
	rawOwn := `{"places":[{"position":1,"title":"Own A","address":"A","placeId":"id-a"},{"position":3,"title":"Own B","address":"B","placeId":"id-b"}]}`
	rawOther := `{"places":[{"position":1,"title":"Other","address":"C","placeId":"id-c"}]}`
	response, err := buildLocalVisibilityPointCompetitors("run-1", 0, competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", rawOwn),
		competitorsRow(1, 0, "success_nonempty", rawOther),
		competitorsRow(0, 1, "request_failed", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.PointIndex == nil || *response.PointIndex != 0 {
		t.Fatalf("point_index = %#v, want 0", response.PointIndex)
	}
	if response.TotalQueryPoints != len(competitorsSnapshot().Queries) {
		t.Fatalf("total_query_points = %d, want one point times frozen queries", response.TotalQueryPoints)
	}
	if response.ContributingQueryPoints != 1 || response.FailedQueryPoints != 1 || response.PendingQueryPoints != 0 {
		t.Fatalf("coverage = %d/%d/%d, want 1/1/0", response.ContributingQueryPoints, response.FailedQueryPoints, response.PendingQueryPoints)
	}
	if len(response.Competitors) != 2 {
		t.Fatalf("competitors = %#v, want only the scoped point's two ids", response.Competitors)
	}
	for _, leaked := range []string{"id-c"} {
		for _, competitor := range response.Competitors {
			if competitor.PlaceID == leaked {
				t.Fatalf("competitor %s leaked from another point: %#v", leaked, response.Competitors)
			}
		}
	}
}

func TestBuildPointCompetitorsDedupeByPlaceIDOverDistinctQueries(t *testing.T) {
	raw := `{"places":[{"position":8,"title":"Dup","address":"D","placeId":"id-a"},{"position":2,"title":"Dup","address":"D","placeId":"id-a"}]}`
	response, err := buildLocalVisibilityPointCompetitors("run-1", 0, competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", raw),
		competitorsRow(0, 1, "success_nonempty", `{"places":[{"position":4,"title":"Dup","address":"D","placeId":"id-a"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 1 {
		t.Fatalf("competitors = %#v, want one id deduped", response.Competitors)
	}
	got := response.Competitors[0]
	if got.QueryPointsSeen != 2 {
		t.Fatalf("query_points_seen = %d, want 2 distinct queries at the point", got.QueryPointsSeen)
	}
	if len(got.QueryIndexes) != 2 || got.QueryIndexes[0] != 0 || got.QueryIndexes[1] != 1 {
		t.Fatalf("query_indexes = %#v, want [0 1]", got.QueryIndexes)
	}
	if got.BestRank == nil || *got.BestRank != 2 {
		t.Fatalf("best_rank = %#v, want 2 (positive min over all entries)", got.BestRank)
	}
}

func TestBuildPointCompetitorsFailedRowsDoNotContribute(t *testing.T) {
	response, err := buildLocalVisibilityPointCompetitors("run-1", 0, competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "request_failed", ""),
		competitorsRow(0, 1, "success_empty", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 0 {
		t.Fatalf("competitors = %#v, want none from failed/empty rows", response.Competitors)
	}
	if response.ContributingQueryPoints != 1 || response.FailedQueryPoints != 1 {
		t.Fatalf("coverage = %d/%d, want 1 contributing / 1 failed", response.ContributingQueryPoints, response.FailedQueryPoints)
	}
}

func TestBuildPointCompetitorsTargetHostFromOtherPoint(t *testing.T) {
	rawRival := competitorsPlacesJSON(competitorsPlaceJSON(2, "id-same", "Rival", "http://example.com"))
	rawTarget := competitorsPlacesJSON(competitorsPlaceJSON(1, "target-place", "Own", "https://Example.com/"))
	response, err := buildLocalVisibilityPointCompetitors("run-1", 0, competitorsSnapshot(), []sqlc.GetLocalVisibilityRunCompetitorResultsRow{
		competitorsRow(0, 0, "success_nonempty", rawRival),
		competitorsRow(2, 1, "success_nonempty", rawTarget),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Competitors) != 1 {
		t.Fatalf("competitors = %#v, want the scoped rival", response.Competitors)
	}
	if !findCompetitor(t, response, "id-same").SameBrandDomain {
		t.Fatalf("same_brand_domain must use target host evidence from any frozen point")
	}
}

func TestBuildPointCompetitorsInvalidPoint(t *testing.T) {
	if _, err := buildLocalVisibilityPointCompetitors("run-1", 5, competitorsSnapshot(), nil); err == nil {
		t.Fatal("want error for a point index absent from the frozen snapshot")
	}
	if _, err := buildLocalVisibilityPointCompetitors("run-1", 0, competitorsSnapshot(), nil); err != nil {
		t.Fatalf("valid point rejected: %v", err)
	}
}

func TestBuildCompetitorsPointIndexScopeField(t *testing.T) {
	global, err := buildLocalVisibilityCompetitors("run-1", competitorsSnapshot(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if value, ok := decoded["point_index"]; !ok || value != nil {
		t.Fatalf("global point_index = %#v, want present null", decoded["point_index"])
	}

	scoped, err := buildLocalVisibilityPointCompetitors("run-1", 2, competitorsSnapshot(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(scoped)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if value, ok := decoded["point_index"].(float64); !ok || value != 2 {
		t.Fatalf("scoped point_index = %#v, want 2", decoded["point_index"])
	}
}

func callGetLocalVisibilityRunPointCompetitors(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, runID, pointIndex string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{
		"projectID":  projectID,
		"locationID": locationID,
		"runID":      runID,
		"pointIndex": pointIndex,
	}, "")
	rr := httptest.NewRecorder()
	app.handleGetLocalVisibilityRunPointCompetitors(rr, req)
	return rr
}

func TestLocalVisibilityPointCompetitorsEndpointAndSecurity(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "point-competitors")
	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"expected_credits":135}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityRunCreatedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created run: %v", err)
	}
	target := "place-point-competitors"
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known,raw_response,error)
		VALUES
		($1,0,0,'success_nonempty','found',2,3,TRUE,$2::jsonb,NULL),
		($1,0,1,'success_nonempty','found',4,3,TRUE,$3::jsonb,NULL)`,
		created.ID,
		`{"places":[{"position":1,"title":"Target Shop","address":"1 Main St","placeId":"`+target+`"},{"position":2,"title":"Rival Zero","address":"2 Main St","placeId":"rival-0"}]}`,
		`{"places":[{"position":3,"title":"Rival One","address":"3 Main St","placeId":"rival-1"}]}`); err != nil {
		t.Fatalf("insert results: %v", err)
	}

	rr = callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "0")
	if rr.Code != http.StatusOK {
		t.Fatalf("point competitors status = %d body=%s", rr.Code, rr.Body.String())
	}
	var zero localVisibilityCompetitorsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &zero); err != nil {
		t.Fatalf("decode point competitors: %v body=%s", err, rr.Body.String())
	}
	if zero.PointIndex == nil || *zero.PointIndex != 0 {
		t.Fatalf("point_index = %#v, want 0", zero.PointIndex)
	}
	if zero.TotalQueryPoints != len(zero.Queries) {
		t.Fatalf("total_query_points = %d, want frozen query count %d", zero.TotalQueryPoints, len(zero.Queries))
	}
	if len(zero.Competitors) != 1 || zero.Competitors[0].PlaceID != "rival-0" {
		t.Fatalf("point 0 competitors = %#v, want only rival-0", zero.Competitors)
	}

	rr = callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "1")
	if rr.Code != http.StatusOK {
		t.Fatalf("point 1 competitors status = %d body=%s", rr.Code, rr.Body.String())
	}
	var one localVisibilityCompetitorsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil {
		t.Fatalf("decode point 1 competitors: %v body=%s", err, rr.Body.String())
	}
	if one.PointIndex == nil || *one.PointIndex != 1 {
		t.Fatalf("point 1 point_index = %#v, want 1", one.PointIndex)
	}
	if len(one.Competitors) != 1 || one.Competitors[0].PlaceID != "rival-1" {
		t.Fatalf("point 1 competitors = %#v, want only rival-1", one.Competitors)
	}

	unknownRun := "00000000-0000-0000-0000-0000000000ff"
	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"unknown run": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, unknownRun, "0")
		},
		"outsider": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.outsider, fx.projectID.String(), location.ID, created.ID, "0")
		},
		"invalid run id": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "not-a-uuid", "0")
		},
		"invalid point index": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "9")
		},
		"non numeric point index": func() *httptest.ResponseRecorder {
			return callGetLocalVisibilityRunPointCompetitors(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID, "x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			rr := call()
			want := http.StatusNotFound
			if strings.Contains(name, "invalid") || strings.Contains(name, "non numeric") {
				want = http.StatusBadRequest
			}
			if rr.Code != want {
				t.Errorf("status = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
			}
		})
	}
}
