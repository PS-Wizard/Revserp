package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/geography"
)

// Location setup API tests: unbound creation, editable query counts, scoped
// listing and deletion, paid Maps listing lookup evidence, deletion guards and
// the free Nominatim cache. Every DB test uses the isolated fixture database
// and local HTTP stubs, never paid calls or live provider URLs.
type mapsCreditBudget struct {
	remaining int64
	reserved  int64
	spent     int64
}

func (fx localVisibilityFixture) platformBudget(t *testing.T) mapsCreditBudget {
	t.Helper()
	var budget mapsCreditBudget
	if err := fx.pool.QueryRow(fx.ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(&budget.remaining, &budget.reserved, &budget.spent); err != nil {
		t.Fatalf("read platform budget: %v", err)
	}
	return budget
}

func (fx localVisibilityFixture) orgBudget(t *testing.T) mapsCreditBudget {
	t.Helper()
	var budget mapsCreditBudget
	if err := fx.pool.QueryRow(fx.ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM organization_maps_credit_budgets WHERE organization_id = $1`, fx.orgID).Scan(&budget.remaining, &budget.reserved, &budget.spent); err != nil {
		t.Fatalf("read org budget: %v", err)
	}
	return budget
}

func createUnboundLocation(t *testing.T, fx localVisibilityFixture, name string) localVisibilityLocationResponse {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"address":"1 Test Street","locality":"Testville","localities":["Testville"],"latitude":27.6942,"longitude":85.3123}`, name)
	rr := callCreateLocation(t, fx.app, fx.ownerID, fx.projectID.String(), body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create unbound location status = %d body=%s", rr.Code, rr.Body.String())
	}
	var location localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
		t.Fatalf("decode unbound location: %v body=%s", err, rr.Body.String())
	}
	if location.PlaceID != nil {
		t.Fatalf("unbound location place_id = %q, want null", *location.PlaceID)
	}
	return location
}

func createEmptyAddressLocation(t *testing.T, fx localVisibilityFixture, name string) localVisibilityLocationResponse {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"latitude":27.6942,"longitude":85.3123}`, name)
	rr := callCreateLocation(t, fx.app, fx.ownerID, fx.projectID.String(), body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create empty-address location status = %d body=%s", rr.Code, rr.Body.String())
	}
	var location localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
		t.Fatalf("decode empty-address location: %v body=%s", err, rr.Body.String())
	}
	if location.PlaceID != nil {
		t.Fatalf("empty-address location place_id = %q, want null", *location.PlaceID)
	}
	if strings.TrimSpace(location.Address) != "" {
		t.Fatalf("empty-address location address = %q, want empty draft", location.Address)
	}
	return location
}

// configureMapsStub points the Maps resolution provider at a local stub. The
// Places endpoint is parked on loopback too so no test can reach a live URL.
func configureMapsStub(fx localVisibilityFixture, endpoint string) {
	fx.app.Config.SerperAPIKey = "test-serper-key"
	fx.app.Config.SerperMapsEndpoint = endpoint
	fx.app.Config.SerperPlacesEndpoint = "http://127.0.0.1:1/local-seo-test/places"
}

// mapsEchoServer stubs the paid Maps endpoint. It asserts the SDK posts
// q/ll/hl and echoes the requested ll back so the strict 10m viewport check
// can pass; the body builder controls credits/places per case.
func mapsEchoServer(t *testing.T, hits *atomic.Int64, status int, buildBody func(ll string) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-API-KEY") != "test-serper-key" {
			t.Errorf("provider request missing X-API-KEY")
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		if strings.TrimSpace(req["q"]) == "" || strings.TrimSpace(req["ll"]) == "" || req["hl"] != "en" {
			t.Errorf("provider request q/ll/hl = %#v, want q, ll and hl=en", req)
		}
		if ll := strings.TrimSpace(req["ll"]); !strings.HasPrefix(ll, "@") {
			t.Errorf("provider ll missing @ prefix: %q", req["ll"])
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(buildBody(req["ll"])))
	}))
}

type mapsObservedCall struct {
	mu sync.Mutex
	q  string
	ll string
}

func (o *mapsObservedCall) store(q, ll string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.q, o.ll = q, ll
}

func (o *mapsObservedCall) load() (string, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.q, o.ll
}

// mapsCaptureServer is mapsEchoServer that also records the last posted q/ll
// so tests can prove the provider received the actual typed name and the LL
// built from the provided viewport.
func mapsCaptureServer(t *testing.T, hits *atomic.Int64, observed *mapsObservedCall, status int, buildBody func(ll string) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-API-KEY") != "test-serper-key" {
			t.Errorf("provider request missing X-API-KEY")
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		if strings.TrimSpace(req["q"]) == "" || strings.TrimSpace(req["ll"]) == "" || req["hl"] != "en" {
			t.Errorf("provider request q/ll/hl = %#v, want q, ll and hl=en", req)
		}
		if ll := strings.TrimSpace(req["ll"]); !strings.HasPrefix(ll, "@") {
			t.Errorf("provider ll missing @ prefix: %q", req["ll"])
		}
		observed.store(req["q"], req["ll"])
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(buildBody(req["ll"])))
	}))
}

func mapsSuccessBody(placesFragment string) func(ll string) string {
	return func(ll string) string {
		return fmt.Sprintf(`{"credits":3,"ll":%q,"places":[%s]}`, ll, placesFragment)
	}
}

func expectedMapsLL(latitude, longitude float64) string {
	return fmt.Sprintf("@%.6f,%.6f,14z", latitude, longitude)
}

// explicitLookupBody builds the direct business-search body: search_query from
// the typed candidate DisplayName (or the explicit search override) plus the
// direct viewport lat/lon. No candidate object and no cached geography.
func explicitLookupBody(search string, candidate geography.GeocodedAddress) string {
	q := strings.TrimSpace(search)
	if q == "" {
		q = strings.TrimSpace(candidate.DisplayName)
	}
	raw, err := json.Marshal(map[string]any{"search_query": q, "latitude": candidate.Latitude, "longitude": candidate.Longitude})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func explicitDirectBody(query string, latitude, longitude float64) string {
	raw, err := json.Marshal(map[string]any{"search_query": query, "latitude": latitude, "longitude": longitude})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func callListProjectLocations(t *testing.T, app *App, userID pgtype.UUID, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID}, "")
	rr := httptest.NewRecorder()
	app.handleListProjectLocations(rr, req)
	return rr
}

func callDeleteProjectLocation(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodDelete, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	app.handleDeleteLocationSetup(rr, req)
	return rr
}

func callDeleteProject(t *testing.T, app *App, userID pgtype.UUID, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodDelete, userID, map[string]string{"projectID": projectID}, "")
	rr := httptest.NewRecorder()
	app.handleDeleteProject(rr, req)
	return rr
}

func callCreateListingLookup(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	return callCreateListingLookupBody(t, app, userID, projectID, locationID, `{}`)
}

func callCreateListingLookupBody(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID, "locationID": locationID}, body)
	rr := httptest.NewRecorder()
	app.handleCreateListingLookup(rr, req)
	return rr
}

func callGetLatestListingLookup(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	app.handleGetLatestListingLookup(rr, req)
	return rr
}

func callBindLocationListing(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID, "locationID": locationID}, body)
	rr := httptest.NewRecorder()
	app.handleBindLocationListing(rr, req)
	return rr
}

func callUnbindLocationListing(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodDelete, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	app.handleUnbindLocationListing(rr, req)
	return rr
}

func callSearchLocationAddress(t *testing.T, app *App, userID pgtype.UUID, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID}, body)
	rr := httptest.NewRecorder()
	app.handleSearchLocationAddress(rr, req)
	return rr
}

func callReverseLocationAddress(t *testing.T, app *App, userID pgtype.UUID, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID}, body)
	rr := httptest.NewRecorder()
	app.handleReverseLocationAddress(rr, req)
	return rr
}

func TestCreateUnboundLocationAndQueryEdits(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)

	// An explicit empty place_id is unbound, not a client-invented identity.
	rr := callCreateLocation(t, fx.app, fx.ownerID, fx.projectID.String(),
		`{"name":"Unbound","address":"1 Test Street","locality":"Testville","localities":["Testville"],"place_id":"","latitude":27.6942,"longitude":85.3123}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create unbound status = %d body=%s", rr.Code, rr.Body.String())
	}
	var location localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
		t.Fatalf("decode location: %v body=%s", err, rr.Body.String())
	}
	if location.PlaceID != nil {
		t.Fatalf("place_id = %q, want null", *location.PlaceID)
	}
	if location.Address != "1 Test Street" || location.Locality != "Testville" || len(location.Localities) != 1 || location.Localities[0] != "Testville" {
		t.Fatalf("location context = %#v", location)
	}
	if len(location.Services) != 0 || len(location.Queries) != 0 {
		t.Fatalf("new location services/queries = %#v/%#v, want empty", location.Services, location.Queries)
	}

	// An empty draft saves no records.
	rr = callUpdateLocationQueries(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `[]`)
	if rr.Code != http.StatusOK {
		t.Fatalf("zero queries status = %d body=%s", rr.Code, rr.Body.String())
	}
	var zeroed []layer4LocationQueryRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &zeroed); err != nil {
		t.Fatalf("decode zeroed draft: %v", err)
	}
	if len(zeroed) != 0 {
		t.Fatalf("zero queries = %#v, want empty", zeroed)
	}

	// A partial four-query edit is valid too.
	rr = callUpdateLocationQueries(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, layer4DraftJSON("a", "b", "c", "d"))
	if rr.Code != http.StatusOK {
		t.Fatalf("partial queries status = %d body=%s", rr.Code, rr.Body.String())
	}
	var partial []layer4LocationQueryRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &partial); err != nil {
		t.Fatalf("decode partial draft: %v", err)
	}
	if len(partial) != 4 || partial[0].Text != "a" || partial[0].Ordinal != 0 {
		t.Fatalf("partial queries = %#v, want four ordered records", partial)
	}

	// Enqueue rejects an unbound location before reserving any credits.
	before := fx.orgBudget(t)
	rr = callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"radius_m":5000,"expected_credits":108}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("unbound enqueue status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	after := fx.orgBudget(t)
	if before != after {
		t.Fatalf("unbound enqueue changed org budget from %#v to %#v", before, after)
	}
}

func TestLocationListScopingAndDeletion(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	first := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "first")
	second := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "second")

	var otherProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'local-visibility-other','https://other.example') RETURNING id`, fx.orgID).Scan(&otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, otherProject) })

	rr := callListProjectLocations(t, fx.app, fx.memberID, fx.projectID.String())
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var listed []localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %d, want 2", len(listed))
	}
	seen := map[string]bool{}
	for _, entry := range listed {
		seen[entry.ID] = true
	}
	if !seen[first.ID] || !seen[second.ID] {
		t.Fatalf("listed ids = %#v", seen)
	}
	if rr := callListProjectLocations(t, fx.app, fx.outsider, fx.projectID.String()); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider list status = %d, want 404", rr.Code)
	}

	if rr := callDeleteProjectLocation(t, fx.app, fx.outsider, fx.projectID.String(), first.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider delete status = %d, want 404", rr.Code)
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, otherProject.String(), first.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("wrong project delete status = %d, want 404", rr.Code)
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), first.ID); rr.Code != http.StatusNoContent {
		t.Fatalf("owner delete status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), first.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rr.Code)
	}

	rr = callListProjectLocations(t, fx.app, fx.ownerID, fx.projectID.String())
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list after delete: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != second.ID {
		t.Fatalf("list after delete = %#v", listed)
	}
}

func TestListingLookupMapsChargedSuccessAndAuthoritativeBinding(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "lookup-success")

	// The saved centre is the search area (27.6942,85.3123). The paid payload
	// returns a strictly different business coordinate (27.7001,85.3188): the
	// regression must keep these unequal so binding cannot pass by echoing the
	// request viewport.
	if location.Latitude == 27.7001 && location.Longitude == 85.3188 {
		t.Fatalf("fixture must keep saved search-area coords different from provider business coords")
	}

	var hits atomic.Int64
	var observed mapsObservedCall
	server := mapsCaptureServer(t, &hits, &observed, http.StatusOK, mapsSuccessBody(
		`{"placeId":"real-place-1","cid":"111","title":"Real Cafe","address":"1 Test Street","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	basePlatform := fx.platformBudget(t)
	baseOrg := fx.orgBudget(t)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v body=%s", err, rr.Body.String())
	}
	if lookup.Status != "completed" || lookup.ExpectedCredits != 3 || lookup.CreditsUsed != 3 || lookup.ReservedCredits != 0 || !lookup.CreditKnown {
		t.Fatalf("lookup = %#v, want completed 3/3/0 known", lookup)
	}
	if len(lookup.Candidates) != 1 || lookup.Candidates[0].PlaceID != "real-place-1" {
		t.Fatalf("candidates = %#v", lookup.Candidates)
	}
	if lookup.Candidates[0].Latitude != 27.7001 || lookup.Candidates[0].Longitude != 85.3188 {
		t.Fatalf("candidate coords = %v,%v, want provider 27.7001,85.3188", lookup.Candidates[0].Latitude, lookup.Candidates[0].Longitude)
	}
	if lookup.Candidates[0].Latitude == location.Latitude && lookup.Candidates[0].Longitude == location.Longitude {
		t.Fatalf("candidate must not equal the saved search-area centre %v,%v", location.Latitude, location.Longitude)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
	if got := fx.platformBudget(t).spent - basePlatform.spent; got != 3 {
		t.Fatalf("platform spent delta = %d, want 3", got)
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 3 {
		t.Fatalf("org spent delta = %d, want 3", got)
	}
	// Fallback uses the saved name+address and the saved search-area viewport.
	if q, ll := observed.load(); q != "lookup-success 1 Test Street" {
		t.Fatalf("provider q = %q, want saved fallback %q", q, "lookup-success 1 Test Street")
	} else if ll != expectedMapsLL(27.6942, 85.3123) {
		t.Fatalf("provider ll = %q, want saved search-area viewport %q", ll, expectedMapsLL(27.6942, 85.3123))
	}

	// A client-invented candidate is rejected without changing the location.
	rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"forged-place"}`, lookup.ID))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("forged bind status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if rr = callGetLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("get after forged bind status = %d", rr.Code)
	}
	var stillUnbound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &stillUnbound); err != nil {
		t.Fatalf("decode unbound: %v", err)
	}
	if stillUnbound.PlaceID != nil {
		t.Fatalf("forged bind changed place_id to %q", *stillUnbound.PlaceID)
	}

	// Binding writes the authoritative Google coordinates from the paid
	// payload, not the saved centre / request viewport.
	rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"real-place-1"}`, lookup.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &bound); err != nil {
		t.Fatalf("decode bound: %v", err)
	}
	if bound.PlaceID == nil || *bound.PlaceID != "real-place-1" {
		t.Fatalf("bound place_id = %v", bound.PlaceID)
	}
	if bound.Latitude != 27.7001 || bound.Longitude != 85.3188 {
		t.Fatalf("binding kept %v,%v, want authoritative provider 27.7001,85.3188", bound.Latitude, bound.Longitude)
	}
	if bound.Latitude == location.Latitude && bound.Longitude == location.Longitude {
		t.Fatalf("bound coords must come from the provider payload, not the request viewport/saved centre")
	}
	if got := fx.platformBudget(t).spent - basePlatform.spent; got != 3 {
		t.Fatalf("binding re-spent: platform spent delta = %d, want 3", got)
	}

	// The latest read is free and never triggers another provider call.
	rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("latest status = %d body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after latest = %d, want 1", hits.Load())
	}

	// Binding is final: further lookups and binds conflict while bound.
	if rr = callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusConflict {
		t.Fatalf("bound lookup status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"real-place-1"}`, lookup.ID)); rr.Code != http.StatusConflict {
		t.Fatalf("second bind status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after bound conflict = %d, want 1", hits.Load())
	}

	// Free unbinding clears identity but preserves coords and lookup history.
	rr = callUnbindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("unbind status = %d body=%s", rr.Code, rr.Body.String())
	}
	var unbound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &unbound); err != nil {
		t.Fatalf("decode unbound: %v", err)
	}
	if unbound.PlaceID != nil {
		t.Fatalf("unbind kept place_id %q, want null", *unbound.PlaceID)
	}
	if unbound.Latitude != 27.7001 || unbound.Longitude != 85.3188 {
		t.Fatalf("unbind moved coords to %v,%v, want preserved 27.7001,85.3188", unbound.Latitude, unbound.Longitude)
	}
	rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("latest after unbind status = %d", rr.Code)
	}
	var history locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if history.ID != lookup.ID || history.Status != "completed" {
		t.Fatalf("history = %#v, want original completed lookup", history)
	}
}

func TestListingLookupSameCandidateDedupedAfterCompleted(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "dedup-completed")

	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, mapsSuccessBody(
		`{"placeId":"real-place-1","cid":"111","title":"Real Cafe","address":"1 Test Street","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)
	baseOrg := fx.orgBudget(t)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("first lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var first locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.Status != "completed" {
		t.Fatalf("first lookup = %#v, want completed", first)
	}

	rr = callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("second lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var second locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if second.ID != first.ID || !second.Deduplicated {
		t.Fatalf("second lookup = %#v, want same evidence id with deduplicated=true", second)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 3 {
		t.Fatalf("org spent delta = %d, want single charge of 3", got)
	}
}

func TestListingLookupSameCandidateDedupedAfterFailed(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "dedup-failed")

	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, mapsSuccessBody(
		`{"cid":"1234567890","title":"CID Only Cafe","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)
	baseOrg := fx.orgBudget(t)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("first lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var first locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.Status != "failed" || first.CreditsUsed != 3 || !first.CreditKnown {
		t.Fatalf("first lookup = %#v, want failed with 3 spent", first)
	}

	rr = callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("second lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var second locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if second.ID != first.ID || !second.Deduplicated || second.Status != "failed" {
		t.Fatalf("second lookup = %#v, want same failed evidence deduplicated", second)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 3 {
		t.Fatalf("org spent delta = %d, want single charge of 3", got)
	}

	// A failed lookup has no bindable candidate, so binding rejects.
	rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"anything"}`, first.ID))
	if rr.Code != http.StatusConflict && rr.Code != http.StatusBadRequest {
		t.Fatalf("bind after failed status = %d, want 4xx; body=%s", rr.Code, rr.Body.String())
	}
}

func TestListingLookupDifferentExplicitQueryReservesAgain(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "explicit-candidate")
	candidateA := geography.GeocodedAddress{DisplayName: fmt.Sprintf("Cafe A Testville %d", time.Now().UnixNano()), Latitude: 27.71, Longitude: 85.33, Locality: "Testville", CountryCode: "np"}
	candidateB := geography.GeocodedAddress{DisplayName: "Cafe B Testville", Latitude: 27.72, Longitude: 85.34, Locality: "Testville", CountryCode: "np"}

	// The paid payload returns business coords strictly different from either
	// explicit viewport, so no bind can pass by echoing the request viewport.
	var hits atomic.Int64
	var observed mapsObservedCall
	server := mapsCaptureServer(t, &hits, &observed, http.StatusOK, mapsSuccessBody(
		`{"placeId":"explicit-place","cid":"222","title":"Explicit Cafe","address":"Testville","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)
	baseOrg := fx.orgBudget(t)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("fallback lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var fallback locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &fallback); err != nil {
		t.Fatalf("decode fallback: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after fallback = %d, want 1", hits.Load())
	}

	// Re-reading evidence is free; nothing retries automatically.
	if rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("latest status = %d", rr.Code)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after latest = %d, want 1 (no auto retry)", hits.Load())
	}

	rr = callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, explicitLookupBody(candidateA.DisplayName, candidateA))
	if rr.Code != http.StatusOK {
		t.Fatalf("explicit A status = %d body=%s", rr.Code, rr.Body.String())
	}
	var explicitA locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &explicitA); err != nil {
		t.Fatalf("decode explicit A: %v", err)
	}
	if explicitA.ID == fallback.ID || explicitA.Deduplicated {
		t.Fatalf("explicit A = %#v, want a new charged lookup", explicitA)
	}
	if hits.Load() != 2 {
		t.Fatalf("provider hits after explicit A = %d, want 2", hits.Load())
	}
	// The provider received the actual typed business name and the LL built
	// from the provided explicit viewport.
	if q, ll := observed.load(); q != candidateA.DisplayName {
		t.Fatalf("provider q = %q, want typed %q", q, candidateA.DisplayName)
	} else if ll != expectedMapsLL(candidateA.Latitude, candidateA.Longitude) {
		t.Fatalf("provider ll = %q, want explicit viewport %q", ll, expectedMapsLL(candidateA.Latitude, candidateA.Longitude))
	}
	if explicitA.Candidates[0].Latitude == candidateA.Latitude && explicitA.Candidates[0].Longitude == candidateA.Longitude {
		t.Fatalf("lookup candidate must come from the provider payload, not the request viewport %v,%v", candidateA.Latitude, candidateA.Longitude)
	}

	rr = callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, explicitLookupBody(candidateA.DisplayName, candidateA))
	if rr.Code != http.StatusOK {
		t.Fatalf("repeat A status = %d body=%s", rr.Code, rr.Body.String())
	}
	var repeatA locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &repeatA); err != nil {
		t.Fatalf("decode repeat A: %v", err)
	}
	if repeatA.ID != explicitA.ID || !repeatA.Deduplicated {
		t.Fatalf("repeat A = %#v, want same evidence deduplicated", repeatA)
	}
	if hits.Load() != 2 {
		t.Fatalf("provider hits after repeat A = %d, want 2", hits.Load())
	}

	rr = callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, explicitLookupBody(candidateB.DisplayName, candidateB))
	if rr.Code != http.StatusOK {
		t.Fatalf("explicit B status = %d body=%s", rr.Code, rr.Body.String())
	}
	var explicitB locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &explicitB); err != nil {
		t.Fatalf("decode explicit B: %v", err)
	}
	if explicitB.ID == explicitA.ID || explicitB.ID == fallback.ID {
		t.Fatalf("explicit B = %#v, want another new lookup", explicitB)
	}
	if hits.Load() != 3 {
		t.Fatalf("provider hits after explicit B = %d, want 3", hits.Load())
	}
	if q, ll := observed.load(); q != candidateB.DisplayName {
		t.Fatalf("provider q = %q, want typed %q", q, candidateB.DisplayName)
	} else if ll != expectedMapsLL(candidateB.Latitude, candidateB.Longitude) {
		t.Fatalf("provider ll = %q, want explicit viewport %q", ll, expectedMapsLL(candidateB.Latitude, candidateB.Longitude))
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 9 {
		t.Fatalf("org spent delta = %d, want 3 lookups x 3 credits", got)
	}
}

func TestListingLookupLegacyCandidateAndInvalidSelectionRejectedBeforeCharge(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "invalid-selection")

	var hits atomic.Int64
	var observed mapsObservedCall
	server := mapsCaptureServer(t, &hits, &observed, http.StatusOK, mapsSuccessBody(
		`{"placeId":"real-place-1","title":"Real Cafe","address":"Testville","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)
	baseOrg := fx.orgBudget(t)
	basePlatform := fx.platformBudget(t)

	// The legacy Nominatim candidate envelope is unknown to the strict body
	// and must be rejected before any reservation or provider call.
	legacy := fmt.Sprintf(`{"search_query":"Real Cafe %d","candidate":{"display_name":"Real Cafe, Testville","latitude":27.71,"longitude":85.33}}`, time.Now().UnixNano())
	if rr := callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, legacy); rr.Code != http.StatusBadRequest {
		t.Fatalf("legacy candidate status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}

	invalidBodies := []struct {
		name string
		body string
	}{
		{"missing pair", `{"search_query":"Typed Cafe","latitude":27.71}`},
		{"missing query falls back but invalid coords", `{"latitude":91,"longitude":85.33}`},
		{"latitude out of range", `{"search_query":"Typed Cafe","latitude":91,"longitude":85.33}`},
		{"longitude out of range", `{"search_query":"Typed Cafe","latitude":27.71,"longitude":-181}`},
		{"oversized query", fmt.Sprintf(`{"search_query":%q,"latitude":27.71,"longitude":85.33}`, strings.Repeat("x", 1001))},
		{"unknown field", `{"search_query":"Typed Cafe","latitude":27.71,"longitude":85.33,"candidate":{}}`},
	}
	for _, tc := range invalidBodies {
		t.Run(tc.name, func(t *testing.T) {
			if rr := callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, tc.body); rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
		})
	}

	if hits.Load() != 0 {
		t.Fatalf("provider hits = %d, want 0 before any valid selection", hits.Load())
	}
	if after := fx.orgBudget(t); after != baseOrg {
		t.Fatalf("invalid selection changed org budget from %#v to %#v", baseOrg, after)
	}
	if after := fx.platformBudget(t); after != basePlatform {
		t.Fatalf("invalid selection changed platform budget from %#v to %#v", basePlatform, after)
	}
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM local_listing_lookups WHERE location_id = $1`, location.ID).Scan(&count); err != nil {
		t.Fatalf("count lookups: %v", err)
	}
	if count != 0 {
		t.Fatalf("lookup rows = %d, want 0", count)
	}

	// A valid uncached typed business query plus an explicit viewport works
	// with no Nominatim seeding: the free geography cache is not consulted.
	typed := fmt.Sprintf("Typed Business %d", time.Now().UnixNano())
	rr := callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, explicitDirectBody(typed, 27.7155, 85.3312))
	if rr.Code != http.StatusOK {
		t.Fatalf("valid uncached lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode valid lookup: %v body=%s", err, rr.Body.String())
	}
	if lookup.Status != "completed" || lookup.CreditsUsed != 3 || !lookup.CreditKnown {
		t.Fatalf("valid lookup = %#v, want completed 3 known", lookup)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
	if q, ll := observed.load(); q != typed {
		t.Fatalf("provider q = %q, want typed %q", q, typed)
	} else if ll != expectedMapsLL(27.7155, 85.3312) {
		t.Fatalf("provider ll = %q, want explicit viewport %q", ll, expectedMapsLL(27.7155, 85.3312))
	}
	if lookup.Candidates[0].Latitude == 27.7155 && lookup.Candidates[0].Longitude == 85.3312 {
		t.Fatalf("candidate must come from the provider payload, not the request viewport")
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 3 {
		t.Fatalf("org spent delta = %d, want 3", got)
	}
}

func TestListingLookupDirectBusinessSearchWithoutSavedAddress(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	// No saved address is required before a direct business search: the draft
	// keeps an empty address and its saved coords are only the search area.
	location := createEmptyAddressLocation(t, fx, "direct-no-address")
	if location.Latitude != 27.6942 || location.Longitude != 85.3123 {
		t.Fatalf("draft centre = %v,%v, want saved search area 27.6942,85.3123", location.Latitude, location.Longitude)
	}

	typed := fmt.Sprintf("Direct Business %d", time.Now().UnixNano())
	const viewportLat, viewportLon = 27.7155, 85.3312

	var hits atomic.Int64
	var observed mapsObservedCall
	server := mapsCaptureServer(t, &hits, &observed, http.StatusOK, mapsSuccessBody(
		`{"placeId":"direct-place-1","cid":"333","title":"Direct Business","address":"Somewhere","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	rr := callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, explicitDirectBody(typed, viewportLat, viewportLon))
	if rr.Code != http.StatusOK {
		t.Fatalf("direct lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode direct lookup: %v body=%s", err, rr.Body.String())
	}
	if lookup.Status != "completed" || lookup.CreditsUsed != 3 || !lookup.CreditKnown {
		t.Fatalf("direct lookup = %#v, want completed 3 known", lookup)
	}
	if q, ll := observed.load(); q != typed {
		t.Fatalf("provider q = %q, want typed %q", q, typed)
	} else if ll != expectedMapsLL(viewportLat, viewportLon) {
		t.Fatalf("provider ll = %q, want provided viewport %q", ll, expectedMapsLL(viewportLat, viewportLon))
	}
	if len(lookup.Candidates) != 1 || lookup.Candidates[0].PlaceID != "direct-place-1" {
		t.Fatalf("candidates = %#v", lookup.Candidates)
	}
	// The empty-address draft is still a search area, not the business
	// coordinate, until the human-confirmed Google bind.
	if rr = callGetLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("get before bind status = %d", rr.Code)
	}
	var before localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &before); err != nil {
		t.Fatalf("decode before bind: %v", err)
	}
	if before.PlaceID != nil {
		t.Fatalf("direct lookup bound place_id %q before human confirmation", *before.PlaceID)
	}
	if before.Latitude != 27.6942 || before.Longitude != 85.3123 {
		t.Fatalf("draft moved to %v,%v before bind, want saved search area", before.Latitude, before.Longitude)
	}

	rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"direct-place-1"}`, lookup.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &bound); err != nil {
		t.Fatalf("decode bound: %v", err)
	}
	if bound.PlaceID == nil || *bound.PlaceID != "direct-place-1" {
		t.Fatalf("bound place_id = %v", bound.PlaceID)
	}
	if bound.Latitude != 27.7001 || bound.Longitude != 85.3188 {
		t.Fatalf("bound coords = %v,%v, want provider payload 27.7001,85.3188", bound.Latitude, bound.Longitude)
	}
	if bound.Latitude == viewportLat && bound.Longitude == viewportLon {
		t.Fatalf("bound coords must come from the provider payload, not the request viewport %v,%v", viewportLat, viewportLon)
	}
	if bound.Latitude == location.Latitude && bound.Longitude == location.Longitude {
		t.Fatalf("bound coords must not equal the saved search-area centre")
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
}

func TestListingLookupFailedCIDOnlyAndMissingCoordsBindRejected(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	cidLocation := createUnboundLocation(t, fx, "cid-only-case")
	coordLocation := createUnboundLocation(t, fx, "missing-coords-case")

	cases := []struct {
		name       string
		locationID string
		places     string
	}{
		{"cid only without placeId fails", cidLocation.ID,
			`{"cid":"1234567890","title":"CID Only Cafe","latitude":27.7001,"longitude":85.3188}`},
		{"placeId without coordinates fails", coordLocation.ID, `{"placeId":"lonely-place","title":"No Coords Cafe"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			server := mapsEchoServer(t, &hits, http.StatusOK, mapsSuccessBody(tc.places))
			t.Cleanup(server.Close)
			configureMapsStub(fx, server.URL)
			baseOrg := fx.orgBudget(t)

			rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), tc.locationID)
			if rr.Code != http.StatusOK {
				t.Fatalf("lookup status = %d body=%s", rr.Code, rr.Body.String())
			}
			var lookup locationListingLookupResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
				t.Fatalf("decode lookup: %v", err)
			}
			// A non-empty result without a bindable placeId still settles the
			// actual 3-credit spend as failed; only a genuine empty array is a
			// completed no-match.
			if lookup.Status != "failed" || lookup.CreditsUsed != 3 || lookup.ReservedCredits != 0 || !lookup.CreditKnown {
				t.Fatalf("lookup = %#v, want failed with 3 spent", lookup)
			}
			if lookup.Error == nil || !strings.Contains(*lookup.Error, "bindable placeId") {
				t.Fatalf("lookup error = %v, want bindable placeId message", lookup.Error)
			}
			if len(lookup.Candidates) != 0 {
				t.Fatalf("failed candidates = %#v, want none exposed", lookup.Candidates)
			}
			if hits.Load() != 1 {
				t.Fatalf("provider hits = %d, want 1", hits.Load())
			}
			if got := fx.orgBudget(t).spent - baseOrg.spent; got != 3 {
				t.Fatalf("org spent delta = %d, want 3", got)
			}

			rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), tc.locationID,
				fmt.Sprintf(`{"lookup_id":%q,"place_id":"lonely-place"}`, lookup.ID))
			if rr.Code != http.StatusConflict && rr.Code != http.StatusBadRequest {
				t.Fatalf("bind after failed status = %d, want 4xx; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestListingLookupGenuineEmptyCompletedNoMatches(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "genuine-empty")

	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, func(ll string) string {
		return fmt.Sprintf(`{"credits":3,"ll":%q,"places":[]}`, ll)
	})
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)
	baseOrg := fx.orgBudget(t)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if lookup.Status != "completed" || lookup.CreditsUsed != 3 || lookup.ReservedCredits != 0 || !lookup.CreditKnown {
		t.Fatalf("lookup = %#v, want completed with 3 spent", lookup)
	}
	if len(lookup.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want no matches", lookup.Candidates)
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 3 {
		t.Fatalf("org spent delta = %d, want 3", got)
	}

	rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"no-such-place"}`, lookup.ID))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bind with no match status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
}

func TestListingLookupNoFundsBlocksProvider(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	fx.exhaustOrganizationAllowance(t)
	location := createUnboundLocation(t, fx, "no-funds")

	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, mapsSuccessBody(
		`{"placeId":"real-place-1","title":"Real Cafe","address":"1 Test Street","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("unfunded lookup status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("provider hits = %d, want 0", hits.Load())
	}
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM local_listing_lookups WHERE location_id = $1`, location.ID).Scan(&count); err != nil {
		t.Fatalf("count lookups: %v", err)
	}
	if count != 0 {
		t.Fatalf("lookup rows = %d, want 0", count)
	}
}

func TestListingLookupChargedHTTPErrorIsFailed(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "charged-error")

	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusInternalServerError, func(ll string) string {
		return `{"credits":3,"places":[]}`
	})
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	base := fx.orgBudget(t)
	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("charged error lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if lookup.Status != "failed" || lookup.CreditsUsed != 3 || lookup.ReservedCredits != 0 || !lookup.CreditKnown {
		t.Fatalf("charged error lookup = %#v", lookup)
	}
	if lookup.Error == nil || *lookup.Error == "" {
		t.Fatalf("charged error lookup error = %v, want recorded", lookup.Error)
	}
	after := fx.orgBudget(t)
	if after.spent-base.spent != 3 || after.reserved != base.reserved {
		t.Fatalf("charged error budget change = spent %d reserved %d, want spent 3 reserved 0", after.spent-base.spent, after.reserved-base.reserved)
	}

	// Reading evidence later must not retry the paid provider.
	if rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("latest status = %d", rr.Code)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after failed lookup = %d, want 1 (no auto retry)", hits.Load())
	}
}

func TestListingLookupUnknownTimeoutHoldsAndBlocks(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "unknown-hold")

	// An undecodable body stands in for a timeout: no charge evidence, so the
	// 3-credit reservation stays held instead of settling or releasing.
	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, func(ll string) string { return `not-a-json-response` })
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	base := fx.orgBudget(t)
	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("unknown lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if lookup.Status != "uncertain" || lookup.CreditKnown || lookup.CreditsUsed != 0 || lookup.ReservedCredits != 3 {
		t.Fatalf("unknown lookup = %#v, want uncertain with 3 held", lookup)
	}
	after := fx.orgBudget(t)
	if after.reserved-base.reserved != 3 || after.spent != base.spent {
		t.Fatalf("unknown lookup budget change = reserved %d spent %d, want reserved 3 spent 0", after.reserved-base.reserved, after.spent-base.spent)
	}

	// The held reservation blocks a retry, even for a different explicit query.
	if rr = callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusConflict {
		t.Fatalf("same retry status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	other := geography.GeocodedAddress{DisplayName: "Other Cafe, Testville", Latitude: 27.73, Longitude: 85.35, Locality: "Testville", CountryCode: "np"}
	if rr = callCreateListingLookupBody(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, explicitLookupBody("Other Cafe, Testville", other)); rr.Code != http.StatusConflict {
		t.Fatalf("different retry status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after blocked retries = %d, want 1", hits.Load())
	}

	// The hold also blocks location and project deletion.
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusConflict {
		t.Fatalf("location delete status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if rr := callDeleteProject(t, fx.app, fx.ownerID, fx.projectID.String()); rr.Code != http.StatusConflict {
		t.Fatalf("project delete status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// The recorded uncertain evidence is immutable and re-readable for free.
	rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("latest status = %d", rr.Code)
	}
	var reread locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &reread); err != nil {
		t.Fatalf("decode latest: %v", err)
	}
	if reread.ID != lookup.ID || reread.Status != "uncertain" || reread.ReservedCredits != 3 {
		t.Fatalf("latest = %#v, want the same held lookup", reread)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after latest = %d, want 1", hits.Load())
	}
}

func TestListingLookupDuplicateActiveConflict(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "duplicate")

	// A running lookup already owns the location's unsettled slot.
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_listing_lookups(location_id,query) VALUES($1,'in-flight')`, location.ID); err != nil {
		t.Fatalf("insert running lookup: %v", err)
	}
	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, mapsSuccessBody(
		`{"placeId":"real-place-1","title":"Real Cafe","address":"1 Test Street","latitude":27.7001,"longitude":85.3188}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	base := fx.orgBudget(t)
	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate lookup status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("provider hits = %d, want 0", hits.Load())
	}
	if after := fx.orgBudget(t); after != base {
		t.Fatalf("duplicate lookup leaked a reservation: %#v -> %#v", base, after)
	}
}

func TestListingLookupCrossScopeNotFound(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	first := createUnboundLocation(t, fx, "scope-first")
	second := createUnboundLocation(t, fx, "scope-second")

	var otherProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'scope-other','https://scope-other.example') RETURNING id`, fx.orgID).Scan(&otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, otherProject) })

	var hits atomic.Int64
	server := mapsEchoServer(t, &hits, http.StatusOK, mapsSuccessBody(
		`{"placeId":"scope-place","cid":"999","title":"Scope Cafe","address":"1 Test Street","latitude":27.7011,"longitude":85.3199}`))
	t.Cleanup(server.Close)
	configureMapsStub(fx, server.URL)

	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), first.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"latest cross location", func() *httptest.ResponseRecorder {
			return callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), second.ID)
		}},
		{"bind cross location", func() *httptest.ResponseRecorder {
			return callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), second.ID, fmt.Sprintf(`{"lookup_id":%q,"place_id":"scope-place"}`, lookup.ID))
		}},
		{"latest outsider", func() *httptest.ResponseRecorder {
			return callGetLatestListingLookup(t, fx.app, fx.outsider, fx.projectID.String(), first.ID)
		}},
		{"lookup outsider", func() *httptest.ResponseRecorder {
			return callCreateListingLookup(t, fx.app, fx.outsider, fx.projectID.String(), first.ID)
		}},
		{"latest wrong project", func() *httptest.ResponseRecorder {
			return callGetLatestListingLookup(t, fx.app, fx.ownerID, otherProject.String(), first.ID)
		}},
		{"bind wrong project", func() *httptest.ResponseRecorder {
			return callBindLocationListing(t, fx.app, fx.ownerID, otherProject.String(), first.ID, fmt.Sprintf(`{"lookup_id":%q,"place_id":"scope-place"}`, lookup.ID))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := tc.call(); rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestLocationDeletionGuardUnsettledLookup(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "guard")

	var lookupID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO local_listing_lookups(location_id,query) VALUES($1,'active') RETURNING id`, location.ID).Scan(&lookupID); err != nil {
		t.Fatalf("insert active lookup: %v", err)
	}

	// An active lookup blocks both location and project deletion.
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusConflict {
		t.Fatalf("active lookup location delete status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if rr := callDeleteProject(t, fx.app, fx.ownerID, fx.projectID.String()); rr.Code != http.StatusConflict {
		t.Fatalf("active lookup project delete status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// An unknown charge with a held reservation blocks deletion too.
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE local_listing_lookups SET status='uncertain', reserved_credits=1 WHERE id=$1`, lookupID); err != nil {
		t.Fatalf("mark lookup uncertain: %v", err)
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusConflict {
		t.Fatalf("uncertain lookup location delete status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// Once the unresolved row is gone, an otherwise settled location deletes.
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM local_listing_lookups WHERE id=$1`, lookupID); err != nil {
		t.Fatalf("delete unresolved lookup: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_listing_lookups(location_id,query,status,reserved_credits,credits_used,credit_known,raw_response)
		VALUES($1,'settled','completed',0,1,TRUE,'{"places":[]}'::jsonb)`, location.ID); err != nil {
		t.Fatalf("insert settled lookup: %v", err)
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusNoContent {
		t.Fatalf("settled lookup location delete status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
}

func TestLocationGeographyCacheAndRefreshFallback(t *testing.T) {
	fx := newLocalVisibilityFixture(t)

	// The permanent cache is a shared singleton keyed by text, so use unique
	// inputs and delete only the keys this test creates.
	seed := time.Now().UnixNano()
	address := fmt.Sprintf("Kathmandu %d", seed)
	searchKey := "search:" + strings.ToLower(address)
	reverseLatitude := 10.0 + float64(seed%100000)/100000.0
	reverseLongitude := 20.0 + float64((seed/100000)%100000)/100000.0
	reverseKey := fmt.Sprintf("reverse:%.6f,%.6f", reverseLatitude, reverseLongitude)
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM location_geography_cache WHERE cache_key IN ($1,$2)`, searchKey, reverseKey)
	})

	var hits atomic.Int64
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			http.Error(w, "provider down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/reverse" {
			_, _ = w.Write([]byte(`{"display_name":"1 Test Street, Kathmandu","lat":"27.6942","lon":"85.3123","address":{"city":"Kathmandu","country_code":"np"}}`))
			return
		}
		_, _ = w.Write([]byte(`[{"display_name":"Kathmandu, Nepal","lat":"27.7172","lon":"85.3240","address":{"city":"Kathmandu","country_code":"np"}}]`))
	}))
	t.Cleanup(server.Close)
	fx.app.Nominatim = geography.NewNominatimClient(server.URL, "revserp-test/1.0")

	// First search populates the permanent cache.
	rr := callSearchLocationAddress(t, fx.app, fx.ownerID, fx.projectID.String(), fmt.Sprintf(`{"address":%q}`, address))
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	var envelope locationGeographyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if envelope.Cached || envelope.RefreshError != "" || len(envelope.Results) != 1 || envelope.Results[0].Locality != "Kathmandu" {
		t.Fatalf("first search envelope = %#v", envelope)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}

	// A repeated search is served from cache without contacting the provider.
	rr = callSearchLocationAddress(t, fx.app, fx.ownerID, fx.projectID.String(), fmt.Sprintf(`{"address":%q}`, strings.ToUpper(address)))
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode cached search: %v", err)
	}
	if !envelope.Cached || len(envelope.Results) != 1 {
		t.Fatalf("cached search envelope = %#v", envelope)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after cached search = %d, want 1", hits.Load())
	}

	// An explicit refresh that fails falls back to the cached data.
	fail.Store(true)
	rr = callSearchLocationAddress(t, fx.app, fx.ownerID, fx.projectID.String(), fmt.Sprintf(`{"address":%q,"refresh":true}`, address))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh fallback status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode refresh fallback: %v", err)
	}
	if !envelope.Cached || envelope.RefreshError == "" || len(envelope.Results) != 1 {
		t.Fatalf("refresh fallback envelope = %#v", envelope)
	}

	// With no cached data an outage surfaces as a provider failure.
	if rr = callSearchLocationAddress(t, fx.app, fx.ownerID, fx.projectID.String(), fmt.Sprintf(`{"address":%q}`, fmt.Sprintf("Unknown Place %d", seed))); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("uncached outage status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}

	// Reverse geocoding caches independently under its own key.
	fail.Store(false)
	if rr = callReverseLocationAddress(t, fx.app, fx.ownerID, fx.projectID.String(), fmt.Sprintf(`{"latitude":%.6f,"longitude":%.6f}`, reverseLatitude, reverseLongitude)); rr.Code != http.StatusOK {
		t.Fatalf("reverse status = %d body=%s", rr.Code, rr.Body.String())
	}
	var reverse locationGeographyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &reverse); err != nil {
		t.Fatalf("decode reverse: %v", err)
	}
	if reverse.Cached || len(reverse.Results) != 1 || reverse.Results[0].DisplayName == "" {
		t.Fatalf("reverse envelope = %#v", reverse)
	}
}

// bindListingLocality clears the permanent reverse cache key a bind test uses,
// before and after, so a prior run cannot satisfy or pollute the case.
func clearReverseGeographyCache(t *testing.T, fx localVisibilityFixture, latitude, longitude float64) {
	t.Helper()
	key := fmt.Sprintf("reverse:%.6f,%.6f", latitude, longitude)
	clear := func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM location_geography_cache WHERE cache_key = $1`, key)
	}
	clear()
	t.Cleanup(clear)
}

// insertCompletedListingLookup stores one settled lookup row directly so a bind
// test can exercise locality persistence without a paid provider call.
func insertCompletedListingLookup(t *testing.T, fx localVisibilityFixture, locationID string, expectedCredits int32, placesFragment string) string {
	t.Helper()
	raw := fmt.Sprintf(`{"places":[%s]}`, placesFragment)
	var lookupID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO local_listing_lookups(location_id,query,status,expected_credits,reserved_credits,credits_used,credit_known,raw_response,completed_at)
		VALUES($1,'bind-locality-test','completed',$2,0,$2,TRUE,$3::jsonb,now()) RETURNING id`, locationID, expectedCredits, raw).Scan(&lookupID); err != nil {
		t.Fatalf("insert completed lookup: %v", err)
	}
	return lookupID.String()
}

func TestBindLocationListingPersistsReverseGeocodedLocalities(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "bind-localities")
	lookupID := insertCompletedListingLookup(t, fx, location.ID, 3,
		`{"placeId":"bind-place-1","title":"Bind Cafe","address":"1 Test Street","latitude":26.5555,"longitude":87.6666}`)
	clearReverseGeographyCache(t, fx, 26.5555, 87.6666)

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Reverse coordinates deliberately differ from the chosen Google ones so
		// the test proves the geocoder never overrides the bound coordinates.
		_, _ = w.Write([]byte(`{"display_name":"Baneshwor, Kathmandu","lat":"27.0000","lon":"85.0000","address":{"suburb":"Baneshwor","city":"Kathmandu","country_code":"np"}}`))
	}))
	t.Cleanup(server.Close)
	fx.app.Nominatim = geography.NewNominatimClient(server.URL, "revserp-test/1.0")

	rr := callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"bind-place-1"}`, lookupID))
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &bound); err != nil {
		t.Fatalf("decode bound: %v", err)
	}
	if bound.Locality != "Baneshwor" || len(bound.Localities) != 2 || bound.Localities[0] != "Baneshwor" || bound.Localities[1] != "Kathmandu" {
		t.Fatalf("bound locality ladder = %q %#v, want smallest-to-widest", bound.Locality, bound.Localities)
	}
	if bound.Latitude != 26.5555 || bound.Longitude != 87.6666 {
		t.Fatalf("bound coords = %v,%v, want chosen 26.5555,87.6666", bound.Latitude, bound.Longitude)
	}
	if hits.Load() != 1 {
		t.Fatalf("reverse provider hits = %d, want 1", hits.Load())
	}

	// A second bind after unbind reuses the cached reverse result.
	if rr = callUnbindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("unbind status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"bind-place-1"}`, lookupID))
	if rr.Code != http.StatusOK {
		t.Fatalf("rebind status = %d body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("reverse provider hits after cached rebind = %d, want 1", hits.Load())
	}
}

func TestBindLocationListingSurvivesReverseProviderFailure(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "bind-provider-down")
	lookupID := insertCompletedListingLookup(t, fx, location.ID, 3,
		`{"placeId":"bind-place-2","title":"Down Cafe","address":"2 Test Street","latitude":28.1111,"longitude":84.2222}`)
	clearReverseGeographyCache(t, fx, 28.1111, 84.2222)

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "provider down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	fx.app.Nominatim = geography.NewNominatimClient(server.URL, "revserp-test/1.0")

	rr := callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"bind-place-2"}`, lookupID))
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &bound); err != nil {
		t.Fatalf("decode bound: %v", err)
	}
	if bound.PlaceID == nil || *bound.PlaceID != "bind-place-2" {
		t.Fatalf("bound place_id = %v, want provider failure to still bind", bound.PlaceID)
	}
	if bound.Locality != "" || len(bound.Localities) != 0 {
		t.Fatalf("geocoder failure wrote locality %q %#v, want empty", bound.Locality, bound.Localities)
	}
	if hits.Load() != 1 {
		t.Fatalf("reverse provider hits = %d, want 1", hits.Load())
	}
	var stored string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT localities::text FROM project_locations WHERE id = $1`, location.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored localities: %v", err)
	}
	if stored != "[]" {
		t.Fatalf("stored localities = %q, want []", stored)
	}
}

func TestBindLocationListingWithoutGeocoderClient(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "bind-no-geocoder")
	lookupID := insertCompletedListingLookup(t, fx, location.ID, 3,
		`{"placeId":"bind-place-3","title":"Offline Cafe","address":"3 Test Street","latitude":29.3333,"longitude":83.4444}`)
	clearReverseGeographyCache(t, fx, 29.3333, 83.4444)
	fx.app.Nominatim = nil

	rr := callBindLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID,
		fmt.Sprintf(`{"lookup_id":%q,"place_id":"bind-place-3"}`, lookupID))
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bound localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &bound); err != nil {
		t.Fatalf("decode bound: %v", err)
	}
	if bound.PlaceID == nil || *bound.PlaceID != "bind-place-3" {
		t.Fatalf("bound place_id = %v, want bind to succeed without a client", bound.PlaceID)
	}
	if bound.Locality != "" || len(bound.Localities) != 0 {
		t.Fatalf("nil client wrote locality %q %#v, want empty", bound.Locality, bound.Localities)
	}
}

func TestEncodeBindListingLocalitiesNeverStoresJSONNull(t *testing.T) {
	for _, localities := range [][]string{nil, {}} {
		encoded, err := encodeBindListingLocalities(localities)
		if err != nil {
			t.Fatalf("encode %#v: %v", localities, err)
		}
		if string(encoded) != "[]" {
			t.Fatalf("locality ladder %#v encoded as %s, want []", localities, encoded)
		}
	}
}
