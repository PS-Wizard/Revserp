package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

const (
	layer3LiveWantDB        = "postgres://local_seo_test:local-seo-test@127.0.0.1:55439/local_seo_test?sslmode=disable"
	layer3LiveOrgID         = "d5f1281b-a451-40b6-9708-46c97f06fd43"
	layer3LiveProjectID     = "8350e98a-60f2-49f5-be8e-91649566fcc1"
	layer3LiveBaselinePlace = "ChIJlSym_K4Z6zkRWAt9oU_H4rA"
	layer3LiveEvidencePath  = "/tmp/local-seo-layer3-live-result.json"

	// Authorized test entity. Typed verbatim into the paid Maps lookup.
	layer3LiveTypedQuery = "Sun Nepal Life Insurance Company Limited"
	// Known real viewport centre: SEARCH AREA only, NOT verified business coords.
	layer3LiveSearchAreaLat = 27.7096675
	layer3LiveSearchAreaLon = 85.3222025
)

func writeLayer3LiveEvidence(t *testing.T, payload map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Logf("layer3 evidence encode: %v", err)
		return
	}
	if err := os.WriteFile(layer3LiveEvidencePath, append(raw, '\n'), 0644); err != nil {
		t.Logf("layer3 evidence write: %v", err)
	}
}

func readPriorLayer3Evidence() map[string]any {
	raw, err := os.ReadFile(layer3LiveEvidencePath)
	if err != nil {
		return nil
	}
	var prior map[string]any
	if err := json.Unmarshal(raw, &prior); err != nil {
		return nil
	}
	return prior
}

// TestLocationLayer3LiveFlow is the revised Layer 3 live contract.
//
//   - NO Nominatim/business-finder search anywhere in this file. The known
//     viewport centre (27.7096675,85.3222025) is a SEARCH AREA, not verified
//     business coordinates.
//   - New-location phase: create one unbound draft (address="", queries=[]),
//     then one explicit 3-credit Maps POST {search_query, latitude, longitude}
//     with the typed term + viewport centre. STOP after resolution; no bind,
//     no grid. Prints every actual Google PID/title/address/coords.
//   - Resume mode (LOCAL_SEO_LAYER3_LOCATION_ID + LOCAL_SEO_LAYER3_GOOGLE_INDEX):
//     zero existing runs, stored completed 3/3/0 lookup, bind stored candidate
//     only after parent confirmation, then 5 queries from known geography
//     (Kamalpokhari, Kathmandu-01, Kathmandu Metropolitan City) and exactly
//     one direct 135-credit executor run.
//
// No paid execution happens unless LOCAL_SEO_LAYER3_LIVE=1 is set explicitly.
func TestLocationLayer3LiveFlow(t *testing.T) {
	if os.Getenv("LOCAL_SEO_LAYER3_LIVE") != "1" {
		t.Skip("LOCAL_SEO_LAYER3_LIVE is not '1'")
	}
	if os.Getenv("LOCAL_SEO_TEST_DATABASE_URL") != layer3LiveWantDB {
		t.Fatalf("refusing: LOCAL_SEO_TEST_DATABASE_URL must be exactly %q", layer3LiveWantDB)
	}
	queries, pool, ctx := newLocalVisibilityTestPool(t)
	_ = godotenv.Load("../../.env")
	cfg := config.Load()
	if strings.TrimSpace(cfg.SerperAPIKey) == "" {
		t.Fatal("SERPER_KEY is required; no live calls performed")
	}
	for _, endpoint := range []string{cfg.SerperMapsEndpoint, cfg.SerperPlacesEndpoint} {
		if !strings.Contains(endpoint, "serper.dev") || strings.Contains(endpoint, "127.0.0.1") || strings.Contains(endpoint, "localhost") {
			t.Fatalf("real Serper endpoints required, got %q", endpoint)
		}
	}
	// Intentionally no Nominatim client: this flow never does a free search.
	app := &App{DB: pool, Queries: queries, Config: cfg}

	var one int
	if err := pool.QueryRow(ctx, `SELECT 1 FROM organizations WHERE id=$1`, layer3LiveOrgID).Scan(&one); err != nil {
		t.Fatalf("live org missing: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT 1 FROM projects WHERE id=$1 AND organization_id=$2`, layer3LiveProjectID, layer3LiveOrgID).Scan(&one); err != nil {
		t.Fatalf("live project missing: %v", err)
	}
	var ownerID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM organization_members WHERE org_id=$1 AND role='owner' ORDER BY created_at LIMIT 1`, layer3LiveOrgID).Scan(&ownerID); err != nil {
		t.Fatalf("live owner membership missing: %v", err)
	}

	// Shared 135-credit grid tail for resume only. Bind is done by the caller;
	// this closure generates/saves the fixed 5 known-geography queries, then
	// enqueues and directly executes exactly one run.
	runGrid := func(locationID string, chosen locationListingCandidate, evidence map[string]any, save func()) {
		levels := []string{"Kamalpokhari", "Kathmandu-01", "Kathmandu Metropolitan City"}
		genBody, _ := json.Marshal(map[string]any{"services": []string{"life insurance"}, "localities": levels})
		genReq := localVisibilityRequest(t, http.MethodPost, ownerID, map[string]string{"projectID": layer3LiveProjectID}, string(genBody))
		genRR := httptest.NewRecorder()
		app.handleGenerateLocationQueries(genRR, genReq)
		if genRR.Code != http.StatusOK {
			evidence["generate_status"] = genRR.Code
			evidence["generate_body"] = genRR.Body.String()
			save()
			t.Fatalf("generate queries status = %d body=%s", genRR.Code, genRR.Body.String())
		}
		var generated struct {
			Queries []string `json:"queries"`
		}
		if err := json.Unmarshal(genRR.Body.Bytes(), &generated); err != nil {
			evidence["generate_body"] = genRR.Body.String()
			save()
			t.Fatalf("decode generated queries: %v body=%s", err, genRR.Body.String())
		}
		if len(generated.Queries) != 5 {
			evidence["generate_body"] = genRR.Body.String()
			save()
			t.Fatalf("generated queries = %d, want exactly 5 from known geography: %s", len(generated.Queries), genRR.Body.String())
		}
		evidence["generated_queries"] = generated.Queries
		evidence["query_localities"] = levels
		evidence["query_service"] = "life insurance"
		evidence["locality_source"] = "known-geography-fixed"
		save()

		updateBody, _ := json.Marshal(map[string]any{"queries": generated.Queries})
		rr := callUpdateLocationQueries(t, app, ownerID, layer3LiveProjectID, locationID, string(updateBody))
		if rr.Code != http.StatusOK {
			evidence["update_queries_status"] = rr.Code
			evidence["update_queries_body"] = rr.Body.String()
			save()
			t.Fatalf("save queries status = %d body=%s", rr.Code, rr.Body.String())
		}
		evidence["queries_saved"] = json.RawMessage(rr.Body.Bytes())
		save()

		rr = callCreateLocalVisibilityRun(t, app, ownerID, layer3LiveProjectID, locationID, `{"radius_m":5000}`)
		if rr.Code != http.StatusAccepted {
			evidence["enqueue_status"] = rr.Code
			evidence["enqueue_body"] = rr.Body.String()
			save()
			t.Fatalf("enqueue status = %d body=%s", rr.Code, rr.Body.String())
		}
		var created localVisibilityRunCreatedResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
			evidence["enqueue_body"] = rr.Body.String()
			save()
			t.Fatalf("decode enqueued run: %v body=%s", err, rr.Body.String())
		}
		if created.ExpectedCredits != 135 || created.ReservedCredits != 135 {
			evidence["enqueue_body"] = rr.Body.String()
			save()
			t.Fatalf("enqueued credits = %d/%d, want 135/135", created.ExpectedCredits, created.ReservedCredits)
		}
		evidence["run_id"] = created.ID
		evidence["enqueue"] = json.RawMessage(rr.Body.Bytes())
		save()
		t.Logf("run=%s expected=%d reserved=%d", created.ID, created.ExpectedCredits, created.ReservedCredits)

		var runID, projectID pgtype.UUID
		if err := runID.Scan(created.ID); err != nil {
			t.Fatalf("scan run id: %v", err)
		}
		if err := projectID.Scan(layer3LiveProjectID); err != nil {
			t.Fatalf("scan project id: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `UPDATE ai_worker_jobs SET status='running', started_at=now() WHERE local_run_id=$1`, runID); err != nil {
			t.Fatalf("mark job running: %v", err)
		}
		executeErr := localvisibility.ExecuteLocalVisibilityRun(context.Background(), pool, cfg, runID, projectID)
		if executeErr == nil {
			_, _ = pool.Exec(context.Background(), `UPDATE ai_worker_jobs SET status='completed', completed_at=now() WHERE local_run_id=$1`, runID)
		} else {
			_, _ = pool.Exec(context.Background(), `UPDATE ai_worker_jobs SET status='failed', error_message=$2, completed_at=now() WHERE local_run_id=$1`, runID, executeErr.Error())
		}
		evidence["execute_error"] = ""
		if executeErr != nil {
			evidence["execute_error"] = executeErr.Error()
		}
		save()

		var snapshotRaw []byte
		var runStatus string
		var expected, reserved, used int
		if err := pool.QueryRow(ctx, `SELECT snapshot, status, expected_credits, reserved_credits, credits_used FROM local_visibility_runs WHERE id=$1`, runID).Scan(&snapshotRaw, &runStatus, &expected, &reserved, &used); err != nil {
			evidence["run_read_error"] = err.Error()
			save()
			t.Fatalf("read run: %v", err)
		}
		var snapshot localvisibility.LocalRunSnapshot
		if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		if len(snapshot.Points) != 9 || len(snapshot.Viewports) != 9 {
			evidence["snapshot"] = json.RawMessage(snapshotRaw)
			save()
			t.Fatalf("snapshot anchors = %d points %d viewports, want 9/9", len(snapshot.Points), len(snapshot.Viewports))
		}
		evidence["snapshot"] = json.RawMessage(snapshotRaw)
		evidence["run_status"] = runStatus
		evidence["run_expected"] = expected
		evidence["run_reserved"] = reserved
		evidence["run_used"] = used
		save()

		rows, err := pool.Query(ctx, `SELECT c.query_index, c.point_index, COALESCE(r.call_status,'pending'), COALESCE(r.match_status,'unknown'), r.rank, COALESCE(r.credits,0), COALESCE(r.credit_known,FALSE), r.error FROM local_run_cells c LEFT JOIN local_visibility_results r USING(run_id,query_index,point_index) WHERE c.run_id=$1 ORDER BY c.query_index, c.point_index`, runID)
		if err != nil {
			t.Fatalf("read cells: %v", err)
		}
		type cellEvidence struct {
			QueryIndex  int     `json:"query_index"`
			PointIndex  int     `json:"point_index"`
			CallStatus  string  `json:"call_status"`
			MatchStatus string  `json:"match_status"`
			Rank        *int32  `json:"rank"`
			Credits     int     `json:"credits"`
			CreditKnown bool    `json:"credit_known"`
			Error       *string `json:"error"`
		}
		cells := []cellEvidence{}
		for rows.Next() {
			var cell cellEvidence
			var rank pgtype.Int4
			var message pgtype.Text
			if err := rows.Scan(&cell.QueryIndex, &cell.PointIndex, &cell.CallStatus, &cell.MatchStatus, &rank, &cell.Credits, &cell.CreditKnown, &message); err != nil {
				rows.Close()
				t.Fatalf("scan cell: %v", err)
			}
			if rank.Valid {
				value := rank.Int32
				cell.Rank = &value
			}
			if message.Valid {
				value := message.String
				cell.Error = &value
			}
			cells = append(cells, cell)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("cells rows: %v", err)
		}
		evidence["cells"] = cells
		var orgRemaining, orgReserved, orgSpent int64
		_ = pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM organization_maps_credit_budgets WHERE organization_id=$1`, layer3LiveOrgID).Scan(&orgRemaining, &orgReserved, &orgSpent)
		var platRemaining, platReserved, platSpent int64
		_ = pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM platform_maps_credit_budget WHERE id=TRUE`).Scan(&platRemaining, &platReserved, &platSpent)
		evidence["org_budget"] = map[string]int64{"remaining": orgRemaining, "reserved": orgReserved, "spent": orgSpent}
		evidence["platform_budget"] = map[string]int64{"remaining": platRemaining, "reserved": platReserved, "spent": platSpent}
		evidence["baseline_place_id"] = layer3LiveBaselinePlace
		evidence["bound_place_id"] = chosen.PlaceID
		evidence["phase"] = "complete"
		save()
		t.Logf("spend status=%q expected=%d reserved=%d used=%d known_cells=%d identity=%q baseline=%q", runStatus, expected, reserved, used, len(cells), chosen.PlaceID, layer3LiveBaselinePlace)
		for _, cell := range cells {
			t.Logf("q%d p%d call=%s match=%s rank=%v credits=%d known=%v", cell.QueryIndex, cell.PointIndex, cell.CallStatus, cell.MatchStatus, cell.Rank, cell.Credits, cell.CreditKnown)
		}
		if executeErr != nil {
			t.Fatalf("live run finished with executor error; evidence retained: %v", executeErr)
		}
	}

	if resumeID := strings.TrimSpace(os.Getenv("LOCAL_SEO_LAYER3_LOCATION_ID")); resumeID != "" {
		rawGoogleIndex := strings.TrimSpace(os.Getenv("LOCAL_SEO_LAYER3_GOOGLE_INDEX"))
		if rawGoogleIndex == "" {
			t.Fatal("resume mode requires explicit LOCAL_SEO_LAYER3_GOOGLE_INDEX")
		}
		googleIndex, err := strconv.Atoi(rawGoogleIndex)
		if err != nil {
			t.Fatalf("LOCAL_SEO_LAYER3_GOOGLE_INDEX: %v", err)
		}

		evidence := map[string]any{
			"mode":         "resume",
			"org_id":       layer3LiveOrgID,
			"project_id":   layer3LiveProjectID,
			"location_id":  resumeID,
			"google_index": googleIndex,
		}
		save := func() { writeLayer3LiveEvidence(t, evidence) }
		if prior := readPriorLayer3Evidence(); prior != nil {
			evidence["prior_resolution_evidence"] = prior
		}

		rr := callGetLocation(t, app, ownerID, layer3LiveProjectID, resumeID)
		if rr.Code != http.StatusOK {
			evidence["get_location_status"] = rr.Code
			evidence["get_location_body"] = rr.Body.String()
			save()
			t.Fatalf("get location status = %d body=%s", rr.Code, rr.Body.String())
		}
		var location localVisibilityLocationResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
			evidence["get_location_body"] = rr.Body.String()
			save()
			t.Fatalf("decode location: %v body=%s", err, rr.Body.String())
		}
		if location.ProjectID != layer3LiveProjectID {
			evidence["location"] = json.RawMessage(rr.Body.Bytes())
			save()
			t.Fatalf("location project = %q, want %q", location.ProjectID, layer3LiveProjectID)
		}
		if !strings.HasPrefix(location.Name, "layer3-live-") {
			evidence["location"] = json.RawMessage(rr.Body.Bytes())
			save()
			t.Fatalf("location %q must use known prefix 'layer3-live-'", location.Name)
		}
		if location.PlaceID != nil {
			evidence["location"] = json.RawMessage(rr.Body.Bytes())
			save()
			t.Fatalf("resume refuses already-bound location place_id=%q; refusing second grid", *location.PlaceID)
		}
		evidence["location"] = json.RawMessage(rr.Body.Bytes())
		save()
		t.Logf("resume location=%s name=%q", location.ID, location.Name)

		var locationUUID pgtype.UUID
		if err := locationUUID.Scan(resumeID); err != nil {
			t.Fatalf("scan resume location id: %v", err)
		}
		var existingRuns int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM local_visibility_runs WHERE location_id=$1`, locationUUID).Scan(&existingRuns); err != nil {
			t.Fatalf("existing runs check: %v", err)
		}
		evidence["existing_runs"] = existingRuns
		save()
		if existingRuns > 0 {
			t.Fatalf("refusing resume: location already has %d local_visibility_runs, want exactly one new run", existingRuns)
		}

		rr = callGetLatestListingLookup(t, app, ownerID, layer3LiveProjectID, resumeID)
		if rr.Code != http.StatusOK {
			evidence["lookup_status_code"] = rr.Code
			evidence["lookup_body"] = rr.Body.String()
			save()
			t.Fatalf("latest lookup status = %d body=%s", rr.Code, rr.Body.String())
		}
		var lookup locationListingLookupResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
			evidence["lookup_body"] = rr.Body.String()
			save()
			t.Fatalf("decode stored lookup: %v body=%s", err, rr.Body.String())
		}
		evidence["lookup_id"] = lookup.ID
		evidence["lookup_response"] = json.RawMessage(rr.Body.Bytes())
		evidence["candidates"] = lookup.Candidates
		save()
		for _, c := range lookup.Candidates {
			t.Logf("stored google candidate place_id=%q title=%q address=%q lat=%v lng=%v", c.PlaceID, c.Title, c.Address, c.Latitude, c.Longitude)
		}
		if lookup.Status != "completed" || !lookup.CreditKnown || lookup.ExpectedCredits != 3 || lookup.CreditsUsed != 3 || lookup.ReservedCredits != 0 {
			save()
			t.Fatalf("STOP: stored lookup not the expected 3-credit completed resolution (status=%q known=%v expected=%d used=%d reserved=%d)", lookup.Status, lookup.CreditKnown, lookup.ExpectedCredits, lookup.CreditsUsed, lookup.ReservedCredits)
		}
		if len(lookup.Candidates) == 0 {
			save()
			t.Fatal("STOP: stored lookup has no candidates")
		}
		if googleIndex < 0 || googleIndex >= len(lookup.Candidates) {
			save()
			t.Fatalf("STOP: google index %d out of %d stored candidates", googleIndex, len(lookup.Candidates))
		}
		chosen := lookup.Candidates[googleIndex]
		if strings.TrimSpace(chosen.PlaceID) == "" {
			save()
			t.Fatal("STOP: chosen stored candidate missing placeId")
		}
		evidence["chosen_candidate"] = chosen
		save()
		t.Logf("resume binds stored candidate[%d] place_id=%q", googleIndex, chosen.PlaceID)

		bindBody, _ := json.Marshal(map[string]string{"lookup_id": lookup.ID, "place_id": chosen.PlaceID})
		rr = callBindLocationListing(t, app, ownerID, layer3LiveProjectID, resumeID, string(bindBody))
		if rr.Code != http.StatusOK {
			evidence["bind_status"] = rr.Code
			evidence["bind_body"] = rr.Body.String()
			save()
			t.Fatalf("bind status = %d body=%s", rr.Code, rr.Body.String())
		}
		var bound localVisibilityLocationResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &bound); err != nil {
			evidence["bind_body"] = rr.Body.String()
			save()
			t.Fatalf("decode bound: %v body=%s", err, rr.Body.String())
		}
		evidence["bound"] = json.RawMessage(rr.Body.Bytes())
		save()
		t.Logf("bound place_id=%v lat=%v lng=%v", bound.PlaceID, bound.Latitude, bound.Longitude)

		runGrid(resumeID, chosen, evidence, save)
		return
	}

	// New-location phase: NO Nominatim search. The viewport centre below is a
	// SEARCH AREA, not verified business coordinates.
	label := strings.TrimSpace(os.Getenv("LOCAL_SEO_LAYER3_LABEL"))
	if label == "" {
		label = fmt.Sprintf("layer3-live-%d", time.Now().UnixNano())
	}
	if !strings.HasPrefix(label, "layer3-live-") {
		t.Fatalf("label %q must use known prefix 'layer3-live-'", label)
	}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_locations WHERE project_id=$1 AND name=$2`, layer3LiveProjectID, label).Scan(&existing); err != nil {
		t.Fatalf("label collision check: %v", err)
	}
	if existing > 0 {
		t.Fatalf("refusing rerun: location label %q already exists", label)
	}

	mapsRequest := map[string]any{
		"search_query": layer3LiveTypedQuery,
		"latitude":     layer3LiveSearchAreaLat,
		"longitude":    layer3LiveSearchAreaLon,
	}
	evidence := map[string]any{
		"mode":                       "new",
		"org_id":                     layer3LiveOrgID,
		"project_id":                 layer3LiveProjectID,
		"label":                      label,
		"typed_search_query":         layer3LiveTypedQuery,
		"search_area_latitude":       layer3LiveSearchAreaLat,
		"search_area_longitude":      layer3LiveSearchAreaLon,
		"search_area_note":           "known real viewport centre is the SEARCH AREA, NOT verified business coordinates",
		"maps_request_search_query":  layer3LiveTypedQuery,
		"maps_request_latitude":      layer3LiveSearchAreaLat,
		"maps_request_longitude":     layer3LiveSearchAreaLon,
		"nominatim_search_performed": false,
	}
	save := func() { writeLayer3LiveEvidence(t, evidence) }
	if prior := readPriorLayer3Evidence(); prior != nil {
		evidence["prior_tmp_evidence"] = prior
	}

	createBody, _ := json.Marshal(map[string]any{
		"name":          label,
		"address":       "",
		"locality":      "Kamalpokhari",
		"query_service": "life insurance",
		"latitude":      layer3LiveSearchAreaLat,
		"longitude":     layer3LiveSearchAreaLon,
		"queries":       []string{},
	})
	rr := callCreateLocation(t, app, ownerID, layer3LiveProjectID, string(createBody))
	if rr.Code != http.StatusCreated {
		evidence["create_status"] = rr.Code
		evidence["create_body"] = rr.Body.String()
		save()
		t.Fatalf("create location status = %d body=%s", rr.Code, rr.Body.String())
	}
	var location localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
		evidence["create_body"] = rr.Body.String()
		save()
		t.Fatalf("decode location: %v body=%s", err, rr.Body.String())
	}
	if location.PlaceID != nil {
		evidence["create_body"] = rr.Body.String()
		save()
		t.Fatalf("new location bound, want unbound: %s", rr.Body.String())
	}
	evidence["location_id"] = location.ID
	evidence["location"] = json.RawMessage(rr.Body.Bytes())
	save()
	t.Logf("draft location=%s name=%q search_area_lat=%v search_area_lng=%v", location.ID, location.Name, layer3LiveSearchAreaLat, layer3LiveSearchAreaLon)

	lookupBody, _ := json.Marshal(mapsRequest)
	evidence["maps_request_body"] = string(lookupBody)
	save()
	rr = callCreateListingLookupBody(t, app, ownerID, layer3LiveProjectID, location.ID, string(lookupBody))
	if rr.Code != http.StatusOK {
		evidence["lookup_status"] = rr.Code
		evidence["lookup_body"] = rr.Body.String()
		save()
		t.Fatalf("listing lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		evidence["lookup_body"] = rr.Body.String()
		save()
		t.Fatalf("decode lookup: %v body=%s", err, rr.Body.String())
	}
	for _, c := range lookup.Candidates {
		t.Logf("google candidate place_id=%q title=%q address=%q lat=%v lng=%v", c.PlaceID, c.Title, c.Address, c.Latitude, c.Longitude)
	}
	evidence["lookup_id"] = lookup.ID
	evidence["lookup_response"] = json.RawMessage(rr.Body.Bytes())
	evidence["lookup"] = lookup
	evidence["candidates"] = lookup.Candidates
	save()
	if lookup.Status == "failed" || lookup.Status == "uncertain" || !lookup.CreditKnown {
		save()
		t.Fatalf("STOP, never retry: lookup status=%q known=%v body=%s", lookup.Status, lookup.CreditKnown, rr.Body.String())
	}
	if lookup.Status != "completed" || lookup.ExpectedCredits != 3 || lookup.CreditsUsed != 3 || lookup.ReservedCredits != 0 {
		save()
		t.Fatalf("STOP, never grid: lookup is not the expected 3-credit completed resolution (status=%q expected=%d used=%d reserved=%d body=%s)", lookup.Status, lookup.ExpectedCredits, lookup.CreditsUsed, lookup.ReservedCredits, rr.Body.String())
	}
	if len(lookup.Candidates) == 0 || strings.TrimSpace(lookup.Candidates[0].PlaceID) == "" {
		save()
		t.Fatalf("STOP, never retry: lookup missing placeId body=%s", rr.Body.String())
	}

	// Intentional stop: persist locationID/lookupID/all Google candidates and
	// wait for parent inspection/confirmation of the actual identity. Never
	// auto-bind and never enqueue the 135-credit grid here; resume with
	// LOCAL_SEO_LAYER3_LOCATION_ID + LOCAL_SEO_LAYER3_GOOGLE_INDEX.
	evidence["phase"] = "resolution-complete-awaiting-identity"
	save()
	t.Logf("STOP after 3-credit resolution: location=%s lookup=%s candidates=%d typed_query=%q search_area=%v,%v; inspect evidence and resume with LOCAL_SEO_LAYER3_LOCATION_ID + LOCAL_SEO_LAYER3_GOOGLE_INDEX before any grid", location.ID, lookup.ID, len(lookup.Candidates), layer3LiveTypedQuery, layer3LiveSearchAreaLat, layer3LiveSearchAreaLon)
}
