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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNewCrawlPageResponseFromListRowOmitsHeavyFields(t *testing.T) {
	var id, crawlID pgtype.UUID
	_ = id.Scan("00000000-0000-0000-0000-000000000010")
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")
	status := pgtype.Int4{Int32: 200, Valid: true}

	row := sqlc.ListCrawlPageSummariesForCrawlByUserRow{
		ID:         id,
		CrawlID:    crawlID,
		Url:        "https://example.com/pricing",
		Title:      pgText("Pricing"),
		StatusCode: status,
	}
	resp := newCrawlPageResponseFromListRow(row)
	if resp.ID != id.String() || resp.CrawlID != crawlID.String() {
		t.Fatalf("ids = %q/%q, want %q/%q", resp.ID, resp.CrawlID, id.String(), crawlID.String())
	}
	if resp.URL != "https://example.com/pricing" || resp.Title != "Pricing" {
		t.Fatalf("url/title = %q/%q", resp.URL, resp.Title)
	}
	if resp.StatusCode == nil || *resp.StatusCode != 200 {
		t.Fatalf("status_code = %+v, want 200", resp.StatusCode)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, heavy := range []string{
		"visible_text", "content_blocks", "h2_headings", "h3_headings",
		"heading_outline", "og_tags", "json_ld",
	} {
		if strings.Contains(string(body), `"`+heavy+`"`) {
			t.Fatalf("list response should omit %q, got %s", heavy, string(body))
		}
	}
	for _, light := range []string{`"id"`, `"crawl_id"`, `"url"`, `"title"`, `"status_code"`} {
		if !strings.Contains(string(body), light) {
			t.Fatalf("list response should include %s, got %s", light, string(body))
		}
	}
}

func newCrawlPageListFixtures(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-page-list-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-page-list-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, outsiderID) })

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'crawl-page-list-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}

	insertPage := func(url, title, createdAgo string) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		err := pool.QueryRow(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, status_code, visible_text, content_blocks, h2_headings, h3_headings, heading_outline, og_tags, json_ld, created_at)
			VALUES ($1,$2,$3,200,'heavy visible text for ' || $2,'[{"type":"p","markdown":"heavy"}]'::jsonb,'["H2"]'::jsonb,'["H3"]'::jsonb,'{"headings":[]}'::jsonb,'{"og:title":"T"}'::jsonb,'[{"@type":"WebPage"}]'::jsonb, now() - ($4::interval))
			RETURNING id`, crawlID, url, pgText(title), createdAgo).Scan(&id)
		if err != nil {
			t.Fatalf("insert page %s: %v", url, err)
		}
		return id
	}
	insertPage("https://example.com/about", "About Us", "10 seconds")
	secondID := insertPage("https://example.com/pricing", "Pricing Page", "0 seconds")

	return queries, pool, ctx, userID, outsiderID, crawlID, secondID
}

func TestListCrawlPageSummariesQueryContracts(t *testing.T) {
	queries, _, ctx, userID, outsiderID, crawlID, _ := newCrawlPageListFixtures(t)

	rows, err := queries.ListCrawlPageSummariesForCrawlByUser(ctx, sqlc.ListCrawlPageSummariesForCrawlByUserParams{
		CrawlID: crawlID, UserID: userID, Limit: 10, Offset: 0,
	})
	if err != nil {
		t.Fatalf("list summaries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(rows))
	}
	if rows[0].Url != "https://example.com/about" || rows[1].Url != "https://example.com/pricing" {
		t.Fatalf("ordering wrong: %q, %q", rows[0].Url, rows[1].Url)
	}
	if !rows[0].Title.Valid || rows[0].Title.String != "About Us" {
		t.Fatalf("title = %+v, want About Us", rows[0].Title)
	}
	if !rows[0].StatusCode.Valid || rows[0].StatusCode.Int32 != 200 {
		t.Fatalf("status = %+v, want 200", rows[0].StatusCode)
	}

	paged, err := queries.ListCrawlPageSummariesForCrawlByUser(ctx, sqlc.ListCrawlPageSummariesForCrawlByUserParams{
		CrawlID: crawlID, UserID: userID, Limit: 1, Offset: 1,
	})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	if len(paged) != 1 || paged[0].Url != "https://example.com/pricing" {
		t.Fatalf("paged rows = %+v, want pricing only", paged)
	}
	total, err := queries.CountCrawlPagesForCrawlByUser(ctx, sqlc.CountCrawlPagesForCrawlByUserParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}

	outsiderRows, err := queries.ListCrawlPageSummariesForCrawlByUser(ctx, sqlc.ListCrawlPageSummariesForCrawlByUserParams{
		CrawlID: crawlID, UserID: outsiderID, Limit: 10, Offset: 0,
	})
	if err != nil {
		t.Fatalf("outsider list: %v", err)
	}
	if len(outsiderRows) != 0 {
		t.Fatalf("outsider should see 0 rows, got %d", len(outsiderRows))
	}
}

func TestHandleListCrawlPagesOmitsHeavyFields(t *testing.T) {
	queries, pool, _, userID, _, crawlID, _ := newCrawlPageListFixtures(t)
	app := &App{DB: pool, Queries: queries}

	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages?limit=10&offset=0", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleListCrawlPages(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}

	var decoded struct {
		Pages      []map[string]json.RawMessage `json:"pages"`
		Pagination struct {
			Limit  int32 `json:"limit"`
			Offset int32 `json:"offset"`
			Count  int32 `json:"count"`
			Total  int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Pages) != 2 {
		t.Fatalf("pages len = %d, want 2, body = %s", len(decoded.Pages), rr.Body.String())
	}
	if decoded.Pagination.Total != 2 || decoded.Pagination.Count != 2 {
		t.Fatalf("pagination = %+v, want count/total 2", decoded.Pagination)
	}
	first := decoded.Pages[0]
	for _, key := range []string{"id", "crawl_id", "url", "title", "status_code"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("first page missing %q: %s", key, rr.Body.String())
		}
	}
	for _, heavy := range []string{
		"visible_text", "content_blocks", "h2_headings", "h3_headings",
		"heading_outline", "og_tags", "json_ld",
	} {
		for _, page := range decoded.Pages {
			if _, ok := page[heavy]; ok {
				t.Fatalf("list page should omit %q: %s", heavy, rr.Body.String())
			}
		}
	}
}

func TestHandleGetCrawlPageKeepsHeavyFields(t *testing.T) {
	queries, pool, _, userID, _, _, secondID := newCrawlPageListFixtures(t)
	app := &App{DB: pool, Queries: queries}

	req := httptest.NewRequest(http.MethodGet, "/crawl-pages/"+secondID.String(), nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("pageID", secondID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleGetCrawlPage(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"visible_text", "content_blocks", "og_tags", "json_ld"} {
		raw, ok := page[key]
		if !ok || string(raw) == "null" || string(raw) == `""` {
			t.Fatalf("detail page should keep %q, got %s", key, rr.Body.String())
		}
	}
}

func TestHandleGetCrawlPageByURLKeepsHeavyFields(t *testing.T) {
	queries, pool, _, userID, _, crawlID, _ := newCrawlPageListFixtures(t)
	app := &App{DB: pool, Queries: queries}

	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/by-url?url="+("https://example.com/pricing"), nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleGetCrawlPageByURL(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"visible_text"`) || !strings.Contains(rr.Body.String(), `"content_blocks"`) {
		t.Fatalf("by-url detail should keep heavy fields, got %s", rr.Body.String())
	}
}
