package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/geography"
)

// Location setup API tests: unbound creation, editable query counts, scoped
// listing and deletion, paid listing lookup evidence, deletion guards and the
// free Nominatim cache. Every DB test is gated by the fixture and the Serper /
// Nominatim providers are local HTTP stubs, never paid calls.

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
	body := fmt.Sprintf(`{"name":%q,"address":"1 Test Street","locality":"Testville","query_service":"coffee","latitude":27.6942,"longitude":85.3123,"queries":["a","b","c","d","e"]}`, name)
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

func configurePlacesStub(fx localVisibilityFixture, endpoint string) {
	fx.app.Config.SerperAPIKey = "test-serper-key"
	fx.app.Config.SerperPlacesEndpoint = endpoint
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
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID, "locationID": locationID}, `{}`)
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
		`{"name":"Unbound","address":"1 Test Street","locality":"Testville","query_service":"coffee","place_id":"","latitude":27.6942,"longitude":85.3123,"queries":["a","b","c","d","e"]}`)
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
	if location.Address != "1 Test Street" || location.Locality != "Testville" || location.QueryService != "coffee" {
		t.Fatalf("location context = %#v", location)
	}

	// Zero queries save an empty list.
	rr = callUpdateLocationQueries(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"queries":[]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("zero queries status = %d body=%s", rr.Code, rr.Body.String())
	}
	var zeroed localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &zeroed); err != nil {
		t.Fatalf("decode zeroed location: %v", err)
	}
	if len(zeroed.Queries) != 0 {
		t.Fatalf("zero queries = %#v, want empty", zeroed.Queries)
	}

	// A partial four-query edit is valid too.
	rr = callUpdateLocationQueries(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"queries":["a","b","c","d"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("partial queries status = %d body=%s", rr.Code, rr.Body.String())
	}
	var partial localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &partial); err != nil {
		t.Fatalf("decode partial location: %v", err)
	}
	if len(partial.Queries) != 4 {
		t.Fatalf("partial queries = %#v, want four", partial.Queries)
	}

	// Enqueue rejects an unbound location before reserving any credits.
	before := fx.orgBudget(t)
	rr = callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"radius_m":5000}`)
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

func TestListingLookupChargedSuccessAndBinding(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "lookup-success")

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-API-KEY") != "test-serper-key" {
			t.Errorf("provider request missing X-API-KEY")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"credits":1,"places":[{"position":1,"title":"Real Cafe","address":"1 Test Street","latitude":27.6942,"longitude":85.3123,"placeId":"real-place-1"}]}`))
	}))
	t.Cleanup(server.Close)
	configurePlacesStub(fx, server.URL)

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
	if lookup.Status != "completed" || lookup.ExpectedCredits != 1 || lookup.CreditsUsed != 1 || lookup.ReservedCredits != 0 || !lookup.CreditKnown {
		t.Fatalf("lookup = %#v", lookup)
	}
	if len(lookup.Candidates) != 1 || lookup.Candidates[0].PlaceID != "real-place-1" {
		t.Fatalf("candidates = %#v", lookup.Candidates)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", hits.Load())
	}
	if got := fx.platformBudget(t).spent - basePlatform.spent; got != 1 {
		t.Fatalf("platform spent delta = %d, want 1", got)
	}
	if got := fx.orgBudget(t).spent - baseOrg.spent; got != 1 {
		t.Fatalf("org spent delta = %d, want 1", got)
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

	// Selecting a real candidate binds without moving the accepted centre.
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
	if bound.Latitude != location.Latitude || bound.Longitude != location.Longitude {
		t.Fatalf("binding changed coordinates from %v,%v to %v,%v", location.Latitude, location.Longitude, bound.Latitude, bound.Longitude)
	}
	if got := fx.platformBudget(t).spent - basePlatform.spent; got != 1 {
		t.Fatalf("binding re-spent: platform spent delta = %d, want 1", got)
	}

	// The latest read is free and never triggers another provider call.
	rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("latest status = %d body=%s", rr.Code, rr.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after latest = %d, want 1", hits.Load())
	}
}

func TestListingLookupNoFundsBlocksProvider(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	fx.exhaustOrganizationAllowance(t)
	location := createUnboundLocation(t, fx, "no-funds")

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"credits":1,"places":[]}`))
	}))
	t.Cleanup(server.Close)
	configurePlacesStub(fx, server.URL)

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"credits":1,"places":[]}`))
	}))
	t.Cleanup(server.Close)
	configurePlacesStub(fx, server.URL)

	base := fx.orgBudget(t)
	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("charged error lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if lookup.Status != "failed" || lookup.CreditsUsed != 1 || lookup.ReservedCredits != 0 || !lookup.CreditKnown {
		t.Fatalf("charged error lookup = %#v", lookup)
	}
	if lookup.Error == nil || *lookup.Error == "" {
		t.Fatalf("charged error lookup error = %v, want recorded", lookup.Error)
	}
	after := fx.orgBudget(t)
	if after.spent-base.spent != 1 || after.reserved != base.reserved {
		t.Fatalf("charged error budget change = spent %d reserved %d, want spent 1 reserved 0", after.spent-base.spent, after.reserved-base.reserved)
	}

	// Reading evidence later must not retry the paid provider.
	if rr = callGetLatestListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("latest status = %d", rr.Code)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits after failed lookup = %d, want 1 (no auto retry)", hits.Load())
	}
}

func TestListingLookupUnknownChargeHoldsReservation(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createUnboundLocation(t, fx, "unknown")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-a-json-response`))
	}))
	t.Cleanup(server.Close)
	configurePlacesStub(fx, server.URL)

	base := fx.orgBudget(t)
	rr := callCreateListingLookup(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("unknown lookup status = %d body=%s", rr.Code, rr.Body.String())
	}
	var lookup locationListingLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if lookup.Status != "uncertain" || lookup.CreditKnown || lookup.CreditsUsed != 0 || lookup.ReservedCredits != 1 {
		t.Fatalf("unknown lookup = %#v, want uncertain with reservation held", lookup)
	}
	after := fx.orgBudget(t)
	if after.reserved-base.reserved != 1 || after.spent != base.spent {
		t.Fatalf("unknown lookup budget change = reserved %d spent %d, want reserved 1 spent 0", after.reserved-base.reserved, after.spent-base.spent)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"credits":1,"places":[]}`))
	}))
	t.Cleanup(server.Close)
	configurePlacesStub(fx, server.URL)

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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"credits":1,"places":[{"placeId":"scope-place","title":"Scope Cafe"}]}`))
	}))
	t.Cleanup(server.Close)
	configurePlacesStub(fx, server.URL)

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
