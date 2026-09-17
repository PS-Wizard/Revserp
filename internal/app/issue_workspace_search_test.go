package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func newWorkspaceSearchFixtures(t *testing.T) (*App, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('workspace-search-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'workspace-search-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var baselineID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now() - interval '1 hour', now() - interval '1 hour') RETURNING id`, projectID).Scan(&baselineID); err != nil {
		t.Fatal(err)
	}
	var currentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}

	insertPage := func(crawlID pgtype.UUID, pageURL, title string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, status_code) VALUES ($1,$2,$3,200)`, crawlID, pageURL, pgText(title)); err != nil {
			t.Fatalf("insert page %s: %v", pageURL, err)
		}
	}
	insertPage(baselineID, "https://example.com/about", "About Us")
	insertPage(currentID, "https://example.com/about", "About Us")
	insertPage(currentID, "https://example.com/blog_100%_special", "100% Special_Blog")

	return &App{DB: pool, Queries: queries}, userID, currentID
}

func callWorkspaceSearch(t *testing.T, app *App, userID, crawlID pgtype.UUID, q string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/issue-workspace/pages/search?q="+url.QueryEscape(q), nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleSearchIssueWorkspacePages(rr, req)
	return rr
}

func decodeWorkspaceSearchPages(t *testing.T, rr *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var decoded struct {
		Pages []map[string]any `json:"pages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v, body = %s", err, rr.Body.String())
	}
	return decoded.Pages
}

// The 513-rune query must be rejected before any database access: an empty
// App (nil DB) proves the handler never reaches the membership or page reads.
func TestHandleSearchIssueWorkspacePagesQueryTooLong(t *testing.T) {
	app := &App{}
	var crawlID pgtype.UUID
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	rr := callWorkspaceSearch(t, app, uid, crawlID, strings.Repeat("a", 513))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "query is too long") {
		t.Fatalf("body = %q, want query is too long", rr.Body.String())
	}
}

func TestHandleSearchIssueWorkspacePagesQuery512Accepted(t *testing.T) {
	app, userID, currentID := newWorkspaceSearchFixtures(t)
	rr := callWorkspaceSearch(t, app, userID, currentID, strings.Repeat("a", 512))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	if pages := decodeWorkspaceSearchPages(t, rr); len(pages) != 0 {
		t.Fatalf("512-char query should match nothing, got %+v", pages)
	}
}

// "%" is a LIKE wildcard but must match literally here, like the crawl page
// search design: only the page whose URL contains a percent sign matches.
func TestHandleSearchIssueWorkspacePagesLiteralPercent(t *testing.T) {
	app, userID, currentID := newWorkspaceSearchFixtures(t)
	rr := callWorkspaceSearch(t, app, userID, currentID, "%")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	pages := decodeWorkspaceSearchPages(t, rr)
	if len(pages) != 1 || pages[0]["url"] != "https://example.com/blog_100%_special" {
		t.Fatalf("literal %% search got %+v, want only the special page", pages)
	}
}

func TestHandleSearchIssueWorkspacePagesUnderscoreAndCase(t *testing.T) {
	app, userID, currentID := newWorkspaceSearchFixtures(t)

	rr := callWorkspaceSearch(t, app, userID, currentID, "_")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	if pages := decodeWorkspaceSearchPages(t, rr); len(pages) != 1 {
		t.Fatalf("literal _ search got %+v, want only the special page", pages)
	}

	rr = callWorkspaceSearch(t, app, userID, currentID, "BLOG_100")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	pages := decodeWorkspaceSearchPages(t, rr)
	if len(pages) != 1 || pages[0]["url"] != "https://example.com/blog_100%_special" {
		t.Fatalf("case-insensitive search got %+v", pages)
	}
}

// An inaccessible or missing workspace crawl must return before any page-row
// read. The App here has a nil page-row store, so reaching the search query
// would panic instead of returning 404.
func TestHandleSearchIssueWorkspacePagesForeignCrawlNotFound(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var memberID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('workspace-search-access-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, memberID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, memberID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('workspace-search-access-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, outsiderID) })

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'workspace-search-access-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var currentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}

	var missingID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&missingID); err != nil {
		t.Fatal(err)
	}

	app := &App{DB: nil, Queries: queries}

	rr := callWorkspaceSearch(t, app, outsiderID, currentID, "")
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "crawl or baseline not found") {
		t.Fatalf("outsider status = %d body = %q, want 404 crawl or baseline not found", rr.Code, rr.Body.String())
	}

	rr = callWorkspaceSearch(t, app, memberID, missingID, "")
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "crawl or baseline not found") {
		t.Fatalf("missing crawl status = %d body = %q, want 404 crawl or baseline not found", rr.Code, rr.Body.String())
	}
}
