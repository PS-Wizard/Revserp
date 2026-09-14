package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywords"
)

func TestKeywordsRouteRegistered(t *testing.T) {
	app := &App{Config: config.Config{}}
	found := false
	if err := chi.Walk(app.Router().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet && strings.Contains(route, "/projects/{projectID}/keywords") {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if !found {
		t.Fatal("GET /projects/{projectID}/keywords is not registered")
	}
}

func TestHandleProjectKeywordsInvalidProjectID(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")
	rr := callProjectKeywords(t, app, userID, "not-a-uuid")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleProjectKeywordsFailOpenAndCoverage(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)
	app := &App{DB: pool, Queries: queries}

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('keywords-handler-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('keywords-handler-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, outsiderID) })

	var emptyProject pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'keywords-empty','https://empty.example') RETURNING id`, orgID).Scan(&emptyProject); err != nil {
		t.Fatal(err)
	}

	rr := callProjectKeywords(t, app, userID, emptyProject.String())
	empty := decodeProjectKeywords(t, rr)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty project status = %d body=%s", rr.Code, rr.Body.String())
	}
	if empty.ProjectID != emptyProject.String() {
		t.Fatalf("project_id = %q, want %q", empty.ProjectID, emptyProject.String())
	}
	if empty.CrawlID != nil {
		t.Fatalf("crawl_id = %v, want null", empty.CrawlID)
	}
	if empty.Seeds == nil || len(empty.Seeds) != 0 {
		t.Fatalf("seeds = %#v, want []", empty.Seeds)
	}

	rr = callProjectKeywords(t, app, outsiderID, emptyProject.String())
	if rr.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d, want 404", rr.Code)
	}

	var seededProject pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'keywords-seeded','https://seeded.example') RETURNING id`, orgID).Scan(&seededProject); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_business_profile (project_id, brand_name, website_url, primary_location, target_keywords)
		VALUES ($1, 'Brand', 'https://seeded.example', 'Midtown', '["plumber"]'::jsonb)
	`, seededProject); err != nil {
		t.Fatal(err)
	}

	rr = callProjectKeywords(t, app, userID, seededProject.String())
	noCrawl := decodeProjectKeywords(t, rr)
	if rr.Code != http.StatusOK {
		t.Fatalf("seeded no-crawl status = %d body=%s", rr.Code, rr.Body.String())
	}
	if noCrawl.CrawlID != nil {
		t.Fatalf("seeded crawl_id = %v, want null", noCrawl.CrawlID)
	}
	if len(noCrawl.Seeds) != 2 {
		t.Fatalf("seeded seeds len = %d, want 2 (plumber + Midtown)", len(noCrawl.Seeds))
	}
	for _, seed := range noCrawl.Seeds {
		if seed.State != keywords.StateNoLandingPage {
			t.Fatalf("seed %q state = %q, want no_landing_page", seed.Keyword, seed.State)
		}
		if seed.Matches == nil {
			t.Fatalf("seed %q matches is null", seed.Keyword)
		}
	}

	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source, completed_at) VALUES ($1,'completed','manual', now()) RETURNING id`, seededProject).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, h1, status_code, content_type) VALUES
		($1, 'https://seeded.example/plumber', 'Emergency plumber', 'Hire a plumber', 200, 'text/html'),
		($1, 'https://seeded.example/pdf', 'plumber brochure', 'plumber', 200, 'application/pdf'),
		($1, 'https://seeded.example/gone', 'plumber 404', 'plumber', 404, 'text/html')
	`, crawlID); err != nil {
		t.Fatal(err)
	}

	rr = callProjectKeywords(t, app, userID, seededProject.String())
	covered := decodeProjectKeywords(t, rr)
	if rr.Code != http.StatusOK {
		t.Fatalf("covered status = %d body=%s", rr.Code, rr.Body.String())
	}
	if covered.CrawlID == nil || *covered.CrawlID != crawlID.String() {
		t.Fatalf("covered crawl_id = %v, want %s", covered.CrawlID, crawlID.String())
	}

	byKeyword := map[string]keywords.Seed{}
	for _, seed := range covered.Seeds {
		byKeyword[seed.Keyword] = seed
	}
	plumber, ok := byKeyword["plumber"]
	if !ok {
		t.Fatalf("missing plumber seed: %#v", covered.Seeds)
	}
	if plumber.State != keywords.StateLikelyTargeted {
		t.Fatalf("plumber state = %q, want likely_targeted", plumber.State)
	}
	if plumber.Geo {
		t.Fatal("plumber should not be geo")
	}
	if len(plumber.Matches) == 0 {
		t.Fatal("plumber matches empty, pdf/404 should have been dropped but html page should hit")
	}
	for _, match := range plumber.Matches {
		if match.URL != "https://seeded.example/plumber" {
			t.Fatalf("plumber matched non-scoreable page %q", match.URL)
		}
	}

	midtown, ok := byKeyword["Midtown"]
	if !ok {
		t.Fatalf("missing Midtown geo seed: %#v", covered.Seeds)
	}
	if !midtown.Geo {
		t.Fatal("Midtown should be geo")
	}
	if midtown.State != keywords.StateNoLandingPage {
		t.Fatalf("Midtown state = %q, want no_landing_page", midtown.State)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO crawls (project_id, status, source, completed_at) VALUES ($1,'completed','competitor', now())`, seededProject); err != nil {
		t.Fatal(err)
	}
	rr = callProjectKeywords(t, app, userID, seededProject.String())
	stillHome := decodeProjectKeywords(t, rr)
	if stillHome.CrawlID == nil || *stillHome.CrawlID != crawlID.String() {
		t.Fatalf("competitor crawl leaked: crawl_id = %v, want home %s", stillHome.CrawlID, crawlID.String())
	}
}

func callProjectKeywords(t *testing.T, app *App, userID pgtype.UUID, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("projectID", projectID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleProjectKeywords(rr, req)
	return rr
}

func decodeProjectKeywords(t *testing.T, rr *httptest.ResponseRecorder) projectKeywordsResponse {
	t.Helper()
	var body projectKeywordsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	return body
}
