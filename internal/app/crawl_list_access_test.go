package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// newCrawlListAccessFixtures builds one crawl owned by a member plus an
// outsider with no membership. Handlers run with the outsider principal must
// report 404 (never 403) so resource existence is not disclosed.
func newCrawlListAccessFixtures(t *testing.T) (*App, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-list-access-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-list-access-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, outsiderID) })

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'crawl-list-access-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,'https://example.com/a',200)`, crawlID); err != nil {
		t.Fatal(err)
	}

	return &App{DB: pool, Queries: queries}, crawlID, outsiderID
}

func callCrawlListRoute(t *testing.T, app *App, handler func(http.ResponseWriter, *http.Request), userID, crawlID pgtype.UUID, rawCrawlID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", rawCrawlID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func TestCrawlListAccessInvalidCrawlID(t *testing.T) {
	app := &App{}
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	var crawlID pgtype.UUID
	handlers := map[string]func(http.ResponseWriter, *http.Request){
		"pages":      app.handleListCrawlPages,
		"issues":     app.handleListCrawlIssues,
		"links":      app.handleListCrawlLinks,
		"site-graph": app.handleGetCrawlSiteGraph,
	}
	for name, handler := range handlers {
		rr := callCrawlListRoute(t, app, handler, uid, crawlID, "not-a-uuid")
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid crawl id") {
			t.Errorf("%s: status = %d body = %q, want 400 invalid crawl id", name, rr.Code, rr.Body.String())
		}
	}
}

func TestCrawlListAccessForeignCrawlNotFound(t *testing.T) {
	app, crawlID, outsiderID := newCrawlListAccessFixtures(t)
	handlers := map[string]func(http.ResponseWriter, *http.Request){
		"pages":      app.handleListCrawlPages,
		"issues":     app.handleListCrawlIssues,
		"links":      app.handleListCrawlLinks,
		"site-graph": app.handleGetCrawlSiteGraph,
	}
	for name, handler := range handlers {
		rr := callCrawlListRoute(t, app, handler, outsiderID, crawlID, crawlID.String())
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404, body = %s", name, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), crawlID.String()) {
			t.Errorf("%s: response leaks crawl id: %s", name, rr.Body.String())
		}
	}
}
