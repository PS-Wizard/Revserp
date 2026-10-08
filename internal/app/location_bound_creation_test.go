package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/googleplaces"
)

// Bound-location tests. Pure selection and radius checks run everywhere; the
// handler tests use the isolated fixture database with a stubbed Places
// endpoint and skip when the test database or migration 101 is absent.

func TestSelectBoundLocationCandidate(t *testing.T) {
	candidates := []locationListingCandidate{
		{PlaceID: "ChIJ-a", Title: "A Cafe", Address: "Street A", Latitude: 27.7, Longitude: 85.3},
		{PlaceID: "ChIJ-b", Title: "B Cafe", Address: "Street B", Latitude: 27.8, Longitude: 85.4},
	}
	chosen, err := selectBoundLocationCandidate(candidates, "  ChIJ-b ")
	if err != nil || chosen.Title != "B Cafe" || chosen.Latitude != 27.8 {
		t.Fatalf("chosen = %#v, %v", chosen, err)
	}
	for _, placeID := range []string{"", "   ", "ChIJ-elsewhere", "chij-b"} {
		if _, err := selectBoundLocationCandidate(candidates, placeID); err == nil {
			t.Fatalf("place_id %q must fail: only an exact saved candidate binds", placeID)
		}
	}
}

func TestBoundLocationRadius(t *testing.T) {
	if radius, err := normalizeLocalVisibilityRadiusM(0); err != nil || radius != 5000 {
		t.Fatalf("omitted radius = %d, %v; want the 5000 default", radius, err)
	}
	for _, radius := range []int{1000, 5000, 25000} {
		if got, err := normalizeLocalVisibilityRadiusM(radius); err != nil || got != radius {
			t.Fatalf("radius %d = %d, %v", radius, got, err)
		}
	}
	for _, radius := range []int{-1, 999, 25001} {
		if _, err := normalizeLocalVisibilityRadiusM(radius); err == nil {
			t.Fatalf("radius %d must fail the 1-25 km grid", radius)
		}
	}
}

// requireLocationListingSearchTables skips handler tests until migration 101
// is applied to the fixture database. Main applies migrations; until then the
// pure tests above are the suite.
func requireLocationListingSearchTables(t *testing.T, fx localVisibilityFixture) {
	t.Helper()
	var regclass string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT to_regclass('public.location_listing_searches')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("location_listing_searches is not migrated in the test database")
	}
	var hasRadius bool
	if err := fx.pool.QueryRow(fx.ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'project_locations' AND column_name = 'radius_m')`).Scan(&hasRadius); err != nil || !hasRadius {
		t.Skip("project_locations.radius_m is not migrated in the test database")
	}
}

// placesNameStub answers the Places Text Search shape with canned listings.
// Nothing here may reach Google.
func placesNameStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("provider method = %q, want POST", r.Method)
		}
		if r.Header.Get("X-Goog-Api-Key") == "" {
			t.Errorf("provider request missing X-Goog-Api-Key")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		query, _ := payload["textQuery"].(string)
		if strings.TrimSpace(query) == "" {
			t.Errorf("provider textQuery missing: %v", payload)
		}
		if _, forbidden := payload["locationBias"]; forbidden {
			t.Errorf("name search must not send locationBias: %v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
}

const boundTestPlacesBody = `{"places":[
	{"id":"ChIJ-bound","displayName":{"text":"Bound Cafe"},"formattedAddress":"1 Test Street","location":{"latitude":27.6942,"longitude":85.3123}},
	{"id":"ChIJ-other","displayName":{"text":"Other Cafe"},"formattedAddress":"2 Test Street","location":{"latitude":27.7,"longitude":85.32}}
]}`

func callSearchLocationListing(t *testing.T, app *App, userID pgtype.UUID, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID}, body)
	rr := httptest.NewRecorder()
	app.handleSearchLocationListing(rr, req)
	return rr
}

func callCreateBoundLocation(t *testing.T, app *App, userID pgtype.UUID, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID}, body)
	rr := httptest.NewRecorder()
	app.handleCreateBoundLocation(rr, req)
	return rr
}

func countBoundLocations(t *testing.T, fx localVisibilityFixture) int {
	t.Helper()
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM project_locations WHERE project_id = $1`, fx.projectID).Scan(&count); err != nil {
		t.Fatalf("count locations: %v", err)
	}
	return count
}

func searchThenBind(t *testing.T, fx localVisibilityFixture) (locationListingSearchResponse, localVisibilityLocationResponse) {
	t.Helper()
	rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"Bound Cafe"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	var search locationListingSearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v body=%s", err, rr.Body.String())
	}
	if search.ExpectedCredits != 0 || len(search.Candidates) != 2 {
		t.Fatalf("search = %#v, want 2 candidates with zero credits", search)
	}
	rr = callCreateBoundLocation(t, fx.app, fx.ownerID, fx.projectID.String(),
		`{"search_id":`+quoteJSON(search.ID)+`,"place_id":"ChIJ-bound","radius_m":5000}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("bound status = %d body=%s", rr.Code, rr.Body.String())
	}
	var location localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
		t.Fatalf("decode bound location: %v body=%s", err, rr.Body.String())
	}
	return search, location
}

func quoteJSON(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestSearchLocationListingSavesProjectOwnedEvidence(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)

	beforePlatform := fx.platformBudget(t)
	beforeOrg := fx.orgBudget(t)
	var beforeLookups int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM local_listing_lookups`).Scan(&beforeLookups); err != nil {
		t.Fatalf("count legacy lookups: %v", err)
	}

	rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"  Bound Cafe "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	var search locationListingSearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v body=%s", err, rr.Body.String())
	}
	if search.ID == "" || search.ExpectedCredits != 0 {
		t.Fatalf("search = %#v, want an id with zero credits", search)
	}
	if len(search.Candidates) != 2 || search.Candidates[0].PlaceID != "ChIJ-bound" || search.Candidates[0].Title != "Bound Cafe" {
		t.Fatalf("candidates = %#v", search.Candidates)
	}

	var stored struct {
		projectID, userID string
		query, status     string
		expiresInFuture   bool
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT project_id::text, user_id::text, query, status, expires_at > now()
		FROM location_listing_searches WHERE id = $1`, search.ID).Scan(
		&stored.projectID, &stored.userID, &stored.query, &stored.status, &stored.expiresInFuture); err != nil {
		t.Fatalf("read saved search: %v", err)
	}
	if stored.projectID != fx.projectID.String() || stored.userID != fx.ownerID.String() {
		t.Fatalf("search ownership = %v/%v, want the project and searching user", stored.projectID, stored.userID)
	}
	if stored.query != "Bound Cafe" || stored.status != "completed" || !stored.expiresInFuture {
		t.Fatalf("stored search = %#v", stored)
	}

	if got := fx.platformBudget(t); got != beforePlatform {
		t.Fatalf("platform budget moved on a zero-credit search: %#v", got)
	}
	if got := fx.orgBudget(t); got != beforeOrg {
		t.Fatalf("org budget moved on a zero-credit search: %#v", got)
	}
	var afterLookups int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM local_listing_lookups`).Scan(&afterLookups); err != nil || afterLookups != beforeLookups {
		t.Fatalf("legacy listing evidence changed: %d -> %d, %v", beforeLookups, afterLookups, err)
	}
}

func TestSearchLocationListingOwnerOnly(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)

	if rr := callSearchLocationListing(t, fx.app, fx.memberID, fx.projectID.String(), `{"query":"Bound Cafe"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("member search status = %d, want 403", rr.Code)
	}
	if rr := callSearchLocationListing(t, fx.app, fx.outsider, fx.projectID.String(), `{"query":"Bound Cafe"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider search status = %d, want 404", rr.Code)
	}
	if rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"   "}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("blank search status = %d, want 400", rr.Code)
	}
	if rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"x","extra":1}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want strict 400", rr.Code)
	}
}

func TestCreateBoundLocationHappyPath(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	fx.fundLocalVisibilityBudgets(t)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)
	beforePlatform := fx.platformBudget(t)
	beforeOrg := fx.orgBudget(t)

	_, location := searchThenBind(t, fx)
	if location.PlaceID == nil || *location.PlaceID != "ChIJ-bound" {
		t.Fatalf("bound place_id = %#v", location.PlaceID)
	}
	if location.Name != "Bound Cafe" || location.Address != "1 Test Street" {
		t.Fatalf("bound metadata = %#v, want the saved listing identity", location)
	}
	if location.Latitude != 27.6942 || location.Longitude != 85.3123 {
		t.Fatalf("bound coordinates = %v,%v, want the saved business coordinates", location.Latitude, location.Longitude)
	}
	if location.RadiusM != 5000 {
		t.Fatalf("exposed radius = %d, want the persisted 5000", location.RadiusM)
	}
	var radius int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT radius_m FROM project_locations WHERE id = $1`, location.ID).Scan(&radius); err != nil || radius != 5000 {
		t.Fatalf("stored radius = %d, %v; want the allowed 5000", radius, err)
	}

	var runs int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM local_visibility_runs WHERE location_id = $1`, location.ID).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("bound creation started %d runs, %v; want none", runs, err)
	}
	if got := fx.platformBudget(t); got != beforePlatform {
		t.Fatalf("platform budget moved on bound creation: %#v", got)
	}
	if got := fx.orgBudget(t); got != beforeOrg {
		t.Fatalf("org budget moved on bound creation: %#v", got)
	}
}

func TestCreateBoundLocationGuards(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)

	search, _ := searchThenBind(t, fx)
	before := countBoundLocations(t, fx)

	boundBody := func(searchID, placeID string, radius int) string {
		return `{"search_id":` + quoteJSON(searchID) + `,"place_id":` + quoteJSON(placeID) + `,"radius_m":` + strconv.Itoa(radius) + `}`
	}
	for _, tc := range []struct {
		name       string
		body       string
		user       pgtype.UUID
		project    string
		wantStatus int
	}{
		{"tampered place id", boundBody(search.ID, "ChIJ-invented", 5000), fx.ownerID, fx.projectID.String(), http.StatusBadRequest},
		{"place id from unbound search is still scoped", boundBody(search.ID, "ChIJ-other", 5000), fx.ownerID, fx.projectID.String(), http.StatusCreated},
		{"radius below minimum", boundBody(search.ID, "ChIJ-other", 500), fx.ownerID, fx.projectID.String(), http.StatusBadRequest},
		{"radius above maximum", boundBody(search.ID, "ChIJ-other", 30000), fx.ownerID, fx.projectID.String(), http.StatusBadRequest},
		{"unknown search", boundBody("00000000-0000-0000-0000-000000000099", "ChIJ-bound", 5000), fx.ownerID, fx.projectID.String(), http.StatusNotFound},
		{"malformed search id", `{"search_id":"nope","place_id":"ChIJ-bound","radius_m":5000}`, fx.ownerID, fx.projectID.String(), http.StatusBadRequest},
		{"member cannot bind", boundBody(search.ID, "ChIJ-other", 5000), fx.memberID, fx.projectID.String(), http.StatusForbidden},
		{"outsider cannot bind", boundBody(search.ID, "ChIJ-other", 5000), fx.outsider, fx.projectID.String(), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := callCreateBoundLocation(t, fx.app, tc.user, tc.project, tc.body); rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}

	var secondProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'bound-foreign','https://bound-foreign.example') RETURNING id`, fx.orgID).Scan(&secondProject); err != nil {
		t.Fatalf("create second project: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = fx.pool.Exec(ctx, `DELETE FROM location_listing_searches WHERE project_id = $1`, secondProject)
		_, _ = fx.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, secondProject)
	})
	if rr := callCreateBoundLocation(t, fx.app, fx.ownerID, secondProject.String(), boundBody(search.ID, "ChIJ-bound", 5000)); rr.Code != http.StatusNotFound {
		t.Fatalf("foreign search status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}

	if got := countBoundLocations(t, fx); got != before+1 {
		t.Fatalf("locations = %d, want exactly one more (the ChIJ-other bind); failures must not store partial rows", got)
	}
}

func TestCreateBoundLocationRejectsExpiredAndFailedSearches(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)

	expiredRaw := `{"places":[{"placeId":"ChIJ-old","title":"Old Cafe","address":"Old Street","latitude":27.7,"longitude":85.3}]}`
	var expiredID string
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO location_listing_searches(project_id, user_id, query, status, raw_response, expires_at)
		VALUES($1,$2,'Old Cafe','completed',$3, now() - interval '1 hour') RETURNING id::text`,
		fx.projectID, fx.ownerID, expiredRaw).Scan(&expiredID); err != nil {
		t.Fatalf("insert expired search: %v", err)
	}
	body := `{"search_id":` + quoteJSON(expiredID) + `,"place_id":"ChIJ-old","radius_m":5000}`
	if rr := callCreateBoundLocation(t, fx.app, fx.ownerID, fx.projectID.String(), body); rr.Code != http.StatusGone {
		t.Fatalf("expired search status = %d, want 410; body=%s", rr.Code, rr.Body.String())
	}

	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	failing := placesNameStub(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	defer failing.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", failing.URL)
	if rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"Boom Cafe"}`); rr.Code != http.StatusBadGateway {
		t.Fatalf("failed provider status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
	var failedID, failedStatus string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT id::text, status FROM location_listing_searches
		WHERE project_id = $1 ORDER BY created_at DESC LIMIT 1`, fx.projectID).Scan(&failedID, &failedStatus); err != nil {
		t.Fatalf("read failed search: %v", err)
	}
	if failedStatus != "failed" {
		t.Fatalf("failed search status = %q, want failed evidence", failedStatus)
	}
	body = `{"search_id":` + quoteJSON(failedID) + `,"place_id":"ChIJ-bound","radius_m":5000}`
	if rr := callCreateBoundLocation(t, fx.app, fx.ownerID, fx.projectID.String(), body); rr.Code != http.StatusConflict {
		t.Fatalf("failed search bind status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateBoundLocationCancelledRequestStoresNothing(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)

	rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"Bound Cafe"}`)
	var search locationListingSearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	before := countBoundLocations(t, fx)

	req := localVisibilityRequest(t, http.MethodPost, fx.ownerID, map[string]string{"projectID": fx.projectID.String()},
		`{"search_id":`+quoteJSON(search.ID)+`,"place_id":"ChIJ-bound","radius_m":5000}`)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	cancelled := httptest.NewRecorder()
	fx.app.handleCreateBoundLocation(cancelled, req.WithContext(ctx))
	if cancelled.Code != http.StatusBadRequest {
		t.Fatalf("cancelled bind status = %d, want 400; body=%s", cancelled.Code, cancelled.Body.String())
	}
	if got := countBoundLocations(t, fx); got != before {
		t.Fatalf("cancelled bind stored a location: %d -> %d", before, got)
	}
}

func requireLocationWebsiteScopeTable(t *testing.T, fx localVisibilityFixture) {
	t.Helper()
	var regclass string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT to_regclass('public.location_website_scopes')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("location_website_scopes is not migrated in the test database")
	}
}

func countWebsiteScopeRevisions(t *testing.T, fx localVisibilityFixture, locationID string) int {
	t.Helper()
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM location_website_scopes WHERE location_id = $1`, locationID).Scan(&count); err != nil {
		t.Fatalf("count website scope revisions: %v", err)
	}
	return count
}

// TestResolveBoundLocationWebsiteScope covers the optional creation scope
// without a database: omitted keeps old behaviour, none clears the URL, exact
// and subtree canonicalize, and a foreign or ambiguous URL is rejected.
func TestResolveBoundLocationWebsiteScope(t *testing.T) {
	parent := "https://local-visibility.example"
	urlPtr := func(value string) *string { return &value }
	for _, tc := range []struct {
		name      string
		scope     *createBoundLocationWebsiteScope
		wantScope bool
		wantURL   any
		wantMatch string
		wantErr   bool
	}{
		{name: "omitted leaves no revision", scope: nil},
		{
			name:      "none clears the url",
			scope:     &createBoundLocationWebsiteScope{Match: "none", URL: urlPtr("https://local-visibility.example/about")},
			wantScope: true, wantURL: nil, wantMatch: "none",
		},
		{
			name:      "exact drops the trailing slash",
			scope:     &createBoundLocationWebsiteScope{Match: "exact", URL: urlPtr("https://local-visibility.example/about/")},
			wantScope: true, wantURL: "https://local-visibility.example/about", wantMatch: "exact",
		},
		{
			name:      "subtree canonicalizes a section url",
			scope:     &createBoundLocationWebsiteScope{Match: "subtree", URL: urlPtr("https://local-visibility.example/locations/springfield/")},
			wantScope: true, wantURL: "https://local-visibility.example/locations/springfield", wantMatch: "subtree",
		},
		{
			name:    "foreign origin is rejected",
			scope:   &createBoundLocationWebsiteScope{Match: "exact", URL: urlPtr("https://foreign.example/about")},
			wantErr: true,
		},
		{
			name:    "http and https are different origins",
			scope:   &createBoundLocationWebsiteScope{Match: "exact", URL: urlPtr("http://local-visibility.example/about")},
			wantErr: true,
		},
		{
			name:    "unknown match is rejected",
			scope:   &createBoundLocationWebsiteScope{Match: "page", URL: urlPtr("https://local-visibility.example/about")},
			wantErr: true,
		},
		{
			name:    "exact without a url is rejected",
			scope:   &createBoundLocationWebsiteScope{Match: "exact"},
			wantErr: true,
		},
		{
			name:    "ambiguous encoded path is rejected",
			scope:   &createBoundLocationWebsiteScope{Match: "subtree", URL: urlPtr("https://local-visibility.example/a%2Fb")},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := resolveBoundLocationWebsiteScope(tc.scope, parent)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got scope %#v", scope)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantScope {
				if scope != nil {
					t.Fatalf("scope = %#v, want nil when the field is omitted", scope)
				}
				return
			}
			if scope == nil || scope.match != tc.wantMatch || scope.url != tc.wantURL {
				t.Fatalf("scope = %#v, want match %q url %#v", scope, tc.wantMatch, tc.wantURL)
			}
		})
	}
}

func TestCreateBoundLocationStoresInitialWebsiteScope(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	requireLocationWebsiteScopeTable(t, fx)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)

	// Old callers omit the field and keep the pre-scope behaviour.
	_, plain := searchThenBind(t, fx)
	if got := countWebsiteScopeRevisions(t, fx, plain.ID); got != 0 {
		t.Fatalf("omitted website_scope created %d revisions, want 0", got)
	}

	bindWithScope := func(scopeJSON string) (localVisibilityLocationResponse, string) {
		rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"Bound Cafe"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
		}
		var search locationListingSearchResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &search); err != nil {
			t.Fatalf("decode search: %v", err)
		}
		body := `{"search_id":` + quoteJSON(search.ID) + `,"place_id":"ChIJ-bound","radius_m":5000,"website_scope":` + scopeJSON + `}`
		rr = callCreateBoundLocation(t, fx.app, fx.ownerID, fx.projectID.String(), body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("bound status = %d body=%s", rr.Code, rr.Body.String())
		}
		var location localVisibilityLocationResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
			t.Fatalf("decode bound location: %v", err)
		}
		return location, rr.Body.String()
	}

	exact, _ := bindWithScope(`{"match":"exact","url":"https://local-visibility.example/about/"}`)
	assertWebsiteScopeRevision(t, fx, exact.ID, 1, "exact", "https://local-visibility.example/about")

	none, _ := bindWithScope(`{"match":"none","url":null}`)
	assertWebsiteScopeRevision(t, fx, none.ID, 1, "none", "")
}

func assertWebsiteScopeRevision(t *testing.T, fx localVisibilityFixture, locationID string, wantRevision int, wantMatch, wantURL string) {
	t.Helper()
	var revision int
	var storedURL *string
	var match string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT revision, url, match FROM location_website_scopes WHERE location_id = $1 ORDER BY revision DESC LIMIT 1`, locationID).Scan(&revision, &storedURL, &match); err != nil {
		t.Fatalf("read initial scope: %v", err)
	}
	if revision != wantRevision || match != wantMatch {
		t.Fatalf("scope = rev %d match %q, want rev %d match %q", revision, match, wantRevision, wantMatch)
	}
	url := ""
	if storedURL != nil {
		url = *storedURL
	}
	if url != wantURL {
		t.Fatalf("scope url = %q, want %q", url, wantURL)
	}
}

func TestCreateBoundLocationRejectsForeignWebsiteScope(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	requireLocationWebsiteScopeTable(t, fx)
	fx.app.Config.GoogleMapsAPIKey = "test-places-key"
	server := placesNameStub(t, http.StatusOK, boundTestPlacesBody)
	defer server.Close()
	fx.app.PlacesListings = googleplaces.NewPlacesListingClient("test-places-key", server.URL)

	rr := callSearchLocationListing(t, fx.app, fx.ownerID, fx.projectID.String(), `{"query":"Bound Cafe"}`)
	var search locationListingSearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	before := countBoundLocations(t, fx)
	body := `{"search_id":` + quoteJSON(search.ID) + `,"place_id":"ChIJ-bound","radius_m":5000,"website_scope":{"match":"exact","url":"https://foreign.example/about"}}`
	if rr := callCreateBoundLocation(t, fx.app, fx.ownerID, fx.projectID.String(), body); rr.Code != http.StatusBadRequest {
		t.Fatalf("foreign scope status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if got := countBoundLocations(t, fx); got != before {
		t.Fatalf("a rejected scope stored a location: %d -> %d", before, got)
	}
}

// TestInsertBoundLocationWithScopeRollsBackOnScopeFailure drives the shared
// insert helper with a scope the database rejects (exact without a url) to
// prove the location and its revision are one transaction: the rollback leaves
// no partial location behind.
func TestInsertBoundLocationWithScopeRollsBackOnScopeFailure(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireLocationListingSearchTables(t, fx)
	requireLocationWebsiteScopeTable(t, fx)
	tx, err := fx.app.DB.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(fx.ctx)) }()
	if _, err := insertBoundLocationWithScope(fx.ctx, tx, fx.projectID, "Atomic Cafe", "ChIJ-atomic", 27.7, 85.3, "", 5000, &boundLocationWebsiteScope{url: nil, match: "exact"}); err == nil {
		t.Fatalf("scope insert with a missing url must fail the check constraint")
	}
	if err := tx.Rollback(fx.ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM project_locations WHERE project_id = $1 AND place_id = 'ChIJ-atomic'`, fx.projectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial location survived a failed scope insert: count=%d err=%v", count, err)
	}
}

func TestCreateBoundLocationRequestWebsiteScopeBodies(t *testing.T) {
	t.Run("omitted scope keeps old callers working", func(t *testing.T) {
		var body createBoundLocationRequest
		if err := json.Unmarshal([]byte(`{"search_id":"a","place_id":"b","radius_m":5000}`), &body); err != nil {
			t.Fatalf("decode omitted: %v", err)
		}
		if body.WebsiteScope != nil {
			t.Fatalf("omitted website_scope = %#v, want nil", body.WebsiteScope)
		}
	})

	t.Run("null scope reads as omitted", func(t *testing.T) {
		var body createBoundLocationRequest
		if err := json.Unmarshal([]byte(`{"search_id":"a","place_id":"b","radius_m":5000,"website_scope":null}`), &body); err != nil {
			t.Fatalf("decode null: %v", err)
		}
		if body.WebsiteScope != nil {
			t.Fatalf("null website_scope = %#v, want nil", body.WebsiteScope)
		}
	})

	t.Run("exact scope keeps the url", func(t *testing.T) {
		var body createBoundLocationRequest
		if err := json.Unmarshal([]byte(`{"search_id":"a","place_id":"b","radius_m":5000,"website_scope":{"match":"exact","url":"https://example.com/about"}}`), &body); err != nil {
			t.Fatalf("decode exact: %v", err)
		}
		if body.WebsiteScope == nil || body.WebsiteScope.Match != "exact" || body.WebsiteScope.URL == nil || *body.WebsiteScope.URL != "https://example.com/about" {
			t.Fatalf("exact website_scope = %#v", body.WebsiteScope)
		}
	})

	t.Run("none scope has no url", func(t *testing.T) {
		var body createBoundLocationRequest
		if err := json.Unmarshal([]byte(`{"search_id":"a","place_id":"b","radius_m":5000,"website_scope":{"match":"none","url":null}}`), &body); err != nil {
			t.Fatalf("decode none: %v", err)
		}
		if body.WebsiteScope == nil || body.WebsiteScope.Match != "none" || body.WebsiteScope.URL != nil {
			t.Fatalf("none website_scope = %#v", body.WebsiteScope)
		}
	})
}

// TestCreateBoundLocationStrictBodyRejectsUnknownFields runs without a
// database: the strict decoder answers before any query, so it proves the new
// optional field does not loosen body validation.
func TestCreateBoundLocationStrictBodyRejectsUnknownFields(t *testing.T) {
	app := &App{}
	userID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	projectID := "00000000-0000-0000-0000-000000000001"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"unknown top-level field", `{"search_id":"a","place_id":"b","radius_m":5000,"extra":1}`},
		{"unknown website_scope field", `{"search_id":"a","place_id":"b","radius_m":5000,"website_scope":{"match":"none","url":null,"extra":1}}`},
		{"website_scope is not an object", `{"search_id":"a","place_id":"b","radius_m":5000,"website_scope":"none"}`},
		{"malformed body", `{"search_id":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := callCreateBoundLocation(t, app, userID, projectID, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// boundScopeStubRow satisfies pgx.Row without a database so the creation
// transaction shape can be tested even when the DB-backed tests skip.
type boundScopeStubRow struct{ err error }

func (row boundScopeStubRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	for _, target := range dest {
		switch value := target.(type) {
		case *pgtype.UUID:
			*value = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
		case *pgtype.Text:
			*value = pgtype.Text{String: "stub", Valid: true}
		case *string:
			*value = "stub"
		case *float64:
			*value = 0
		case *pgtype.Timestamptz:
			*value = pgtype.Timestamptz{Valid: true}
		case *[]byte:
			*value = []byte("[]")
		case *int32:
			*value = 0
		default:
			return fmt.Errorf("unexpected scan target %T", target)
		}
	}
	return nil
}

// boundScopeStubTx records the SQL the helper runs and can fail the scope
// insert, so the one-transaction order is asserted without a database.
type boundScopeStubTx struct {
	pgx.Tx
	statements []string
	scopeError error
}

func (tx *boundScopeStubTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	tx.statements = append(tx.statements, sql)
	if strings.Contains(sql, "INSERT INTO location_website_scopes") {
		return boundScopeStubRow{err: tx.scopeError}
	}
	return boundScopeStubRow{}
}

func TestInsertBoundLocationWithScopeTransactionShape(t *testing.T) {
	scopeFailure := errors.New("scope insert failed")

	t.Run("a scope failure surfaces so the caller rolls back", func(t *testing.T) {
		tx := &boundScopeStubTx{scopeError: scopeFailure}
		_, err := insertBoundLocationWithScope(context.Background(), tx, pgtype.UUID{Valid: true},
			"Atomic Cafe", "ChIJ-atomic", 27.7, 85.3, "", 5000,
			&boundLocationWebsiteScope{url: "https://local-visibility.example/about", match: "exact"})
		if !errors.Is(err, scopeFailure) {
			t.Fatalf("err = %v, want the scope insert failure", err)
		}
		if len(tx.statements) != 2 ||
			!strings.Contains(tx.statements[0], "INSERT INTO project_locations") ||
			!strings.Contains(tx.statements[1], "INSERT INTO location_website_scopes") {
			t.Fatalf("statements = %#v, want location then scope on one transaction", tx.statements)
		}
	})

	t.Run("an omitted scope writes only the location", func(t *testing.T) {
		tx := &boundScopeStubTx{}
		if _, err := insertBoundLocationWithScope(context.Background(), tx, pgtype.UUID{Valid: true},
			"Atomic Cafe", "ChIJ-atomic", 27.7, 85.3, "", 5000, nil); err != nil {
			t.Fatalf("err = %v, want success", err)
		}
		if len(tx.statements) != 1 {
			t.Fatalf("statements = %#v, want only the location insert", tx.statements)
		}
	})
}
