package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestHandleSearchCrawlPagesInvalidCrawlID(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/crawls/not-a-uuid/pages/search", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleSearchCrawlPages(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid crawl id") {
		t.Fatalf("body = %q, want invalid crawl id", rr.Body.String())
	}
}

func TestHandleSearchCrawlPagesQueryTooLong(t *testing.T) {
	app := &App{}
	var crawlID pgtype.UUID
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	q := strings.Repeat("a", 513)
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/search?q="+q, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleSearchCrawlPages(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "query is too long") {
		t.Fatalf("body = %q, want query is too long", rr.Body.String())
	}
}

func TestHandleSearchCrawlPagesQueryExactly512(t *testing.T) {
	q := strings.Repeat("a", 512)
	if utf8.RuneCountInString(q) > 512 {
		t.Fatalf("512 runes should be allowed")
	}
	q2 := strings.Repeat("a", 513)
	if utf8.RuneCountInString(q2) <= 512 {
		t.Fatalf("513 runes should be too long")
	}
}
func TestHandleSearchCrawlPagesUnicodeQueryTooLong(t *testing.T) {
	app := &App{}
	var crawlID pgtype.UUID
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	q := strings.Repeat("é", 513) // 2 bytes each, 513 runes
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/search?q="+q, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleSearchCrawlPages(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "query is too long") {
		t.Fatalf("status = %d body = %q, want 400 query is too long", rr.Code, rr.Body.String())
	}
}

func TestHandleSearchCrawlPagesInvalidLimit(t *testing.T) {
	app := &App{}
	var crawlID pgtype.UUID
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/search?limit=0", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleSearchCrawlPages(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid limit") {
		t.Fatalf("status = %d body = %q, want invalid limit", rr.Code, rr.Body.String())
	}
}

func TestHandleGetCrawlPageHealthDetailInvalidCrawlID(t *testing.T) {
	app := &App{}
	var pageID pgtype.UUID
	_ = pageID.Scan("00000000-0000-0000-0000-000000000003")
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/crawls/not-a-uuid/pages/"+pageID.String()+"/health", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", "not-a-uuid")
	routeCtx.URLParams.Add("pageID", pageID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleGetCrawlPageHealthDetail(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid crawl id") {
		t.Fatalf("status = %d body = %q, want invalid crawl id", rr.Code, rr.Body.String())
	}
}

func TestHandleGetCrawlPageHealthDetailInvalidPageID(t *testing.T) {
	app := &App{}
	var crawlID pgtype.UUID
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/not-a-uuid/health", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	routeCtx.URLParams.Add("pageID", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleGetCrawlPageHealthDetail(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid page id") {
		t.Fatalf("status = %d body = %q, want invalid page id", rr.Code, rr.Body.String())
	}
}

func TestBuildCrawlPageSearchItem(t *testing.T) {
	var id pgtype.UUID
	_ = id.Scan("00000000-0000-0000-0000-000000000010")
	title := pgtype.Text{String: "Hello", Valid: true}
	item := buildCrawlPageSearchItem(id, "https://example.com", title)
	if item.ID != id.String() || item.URL != "https://example.com" || item.Title == nil || *item.Title != "Hello" {
		t.Fatalf("item = %+v", item)
	}
	empty := pgtype.Text{Valid: false}
	item2 := buildCrawlPageSearchItem(id, "https://example.com", empty)
	if item2.Title != nil {
		t.Fatalf("expected nil title, got %+v", item2)
	}
}

func TestCrawlPageSearchResponseShape(t *testing.T) {
	title := "Example"
	resp := crawlPageSearchResponse{
		CrawlID: "00000000-0000-0000-0000-000000000002",
		Query:   "test",
		Pages: []crawlPageSearchItem{
			{ID: "00000000-0000-0000-0000-000000000010", URL: "https://example.com", Title: &title},
			{ID: "00000000-0000-0000-0000-000000000011", URL: "https://example.com/other"},
		},
		Pagination: paginationResponse{Limit: 50, Offset: 0, Count: 2, Total: 10},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"crawl_id", "query", "pages", "pagination"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing key %q in %s", key, string(b))
		}
	}
	var pages []map[string]any
	if err := json.Unmarshal(m["pages"], &pages); err != nil {
		t.Fatalf("pages unmarshal: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages len = %d", len(pages))
	}
	if pages[0]["title"] != "Example" {
		t.Fatalf("first page title = %v", pages[0]["title"])
	}
	if _, ok := pages[1]["title"]; ok {
		t.Fatalf("second page should omit title, got %v", pages[1])
	}
	if _, ok := pages[0]["health_score"]; ok {
		t.Fatalf("search should never include health_score")
	}
	var pagination map[string]any
	if err := json.Unmarshal(m["pagination"], &pagination); err != nil {
		t.Fatalf("pagination unmarshal: %v", err)
	}
	for _, key := range []string{"limit", "offset", "count", "total"} {
		if _, ok := pagination[key]; !ok {
			t.Fatalf("pagination missing %q", key)
		}
	}
}

func TestCrawlPageHealthDetailResponseShape(t *testing.T) {
	resp := crawlPageHealthDetailResponse{
		CrawlID:     "00000000-0000-0000-0000-000000000002",
		PageID:      "00000000-0000-0000-0000-000000000003",
		URL:         "https://example.com",
		HealthScore: 85,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"crawl_id", "page_id", "url", "health_score"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing key %q", key)
		}
	}
	if _, ok := m["scoring_version"]; ok {
		t.Fatalf("should not include scoring_version, got %s", string(b))
	}
	var hs struct {
		HealthScore int `json:"health_score"`
	}
	if err := json.Unmarshal(b, &hs); err != nil {
		t.Fatalf("unmarshal health_score: %v", err)
	}
	if hs.HealthScore != 85 {
		t.Fatalf("health_score = %d", hs.HealthScore)
	}
}

// A 512-rune query must be accepted end to end (513 is rejected above).
func TestHandleSearchCrawlPagesQuery512Accepted(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-search-512-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'crawl-search-512-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}

	app := &App{DB: pool, Queries: queries}
	q := strings.Repeat("a", 512)
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/search?q="+q, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	reqCtx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	reqCtx = withPrincipal(reqCtx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(reqCtx)
	rr := httptest.NewRecorder()
	app.handleSearchCrawlPages(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
}
