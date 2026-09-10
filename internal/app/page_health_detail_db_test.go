package app

// Regression tests for the detailed page-health response contract:
//
//	GET /crawls/{crawlID}/pages/{pageID}/health
//
// Contract under test:
//   - Existing fields remain crawl_id, page_id, url, health_score.
//   - New `pillars` array contains deterministic objects
//     {id, score, buckets:[{id, score}]} whose values match the persisted
//     health_score / health_breakdown exactly.
//   - An old/unscored page with NULL health_score or NULL health_breakdown
//     returns 404.
//   - Tenancy boundary remains 404 (wrong crawl, outside org).
//   - Terminal crawls keep immutable-cache behavior; live crawls stay no-store.
//
// These tests assert the contract through the HTTP handler (JSON on the wire),
// so they compile and run independently of the production response struct.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// pageHealthDetailPillarBucket mirrors one wire bucket {id, score}.
type pageHealthDetailPillarBucket struct {
	ID    string `json:"id"`
	Score int    `json:"score"`
}

// pageHealthDetailPillar mirrors one wire pillar {id, score, buckets}.
type pageHealthDetailPillar struct {
	ID      string                         `json:"id"`
	Score   int                            `json:"score"`
	Buckets []pageHealthDetailPillarBucket `json:"buckets"`
}

// pageHealthDetailWire mirrors the wire response. Extra production fields are
// tolerated; the assertions below pin the contracted ones.
type pageHealthDetailWire struct {
	CrawlID     string                   `json:"crawl_id"`
	PageID      string                   `json:"page_id"`
	URL         string                   `json:"url"`
	HealthScore int                      `json:"health_score"`
	Pillars     []pageHealthDetailPillar `json:"pillars"`
}

const pageHealthDetailTestBreakdown = `{"pillars":[
	{"id":"seo","score":80,"buckets":[{"id":"headings","score":90},{"id":"meta_tags","score":70}]},
	{"id":"aeo","score":85,"buckets":[{"id":"answerability","score":85}]},
	{"id":"pagespeed","score":60,"buckets":[{"id":"cls","score":75},{"id":"lcp","score":55}]}
]}`

func setupPageHealthDetailTest(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var ownerID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('page-health-detail-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, ownerID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('page-health-detail-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, outsiderID) })

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'page-health-detail-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	return queries, pool, ctx, ownerID, outsiderID, projectID
}

func insertPageHealthDetailCrawl(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID pgtype.UUID, status string) pgtype.UUID {
	t.Helper()
	var crawlID pgtype.UUID
	if isCrawlStatusTerminal(status) {
		if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,$2, now(), now()) RETURNING id`, projectID, status).Scan(&crawlID); err != nil {
			t.Fatal(err)
		}
		return crawlID
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, created_at) VALUES ($1,$2, now()) RETURNING id`, projectID, status).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	return crawlID
}

// insertPageHealthDetailPage inserts one crawl page. A nil score stores NULL
// health_score; an empty breakdown stores NULL health_breakdown.
func insertPageHealthDetailPage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, crawlID pgtype.UUID, pageURL string, score *int16, breakdown string) pgtype.UUID {
	t.Helper()
	var hs pgtype.Int2
	if score != nil {
		hs = pgtype.Int2{Int16: *score, Valid: true}
	}
	var bd any
	if breakdown != "" {
		bd = breakdown
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, health_score, health_breakdown, status_code) VALUES ($1,$2,$3,$4,$5::jsonb,200) RETURNING id`, crawlID, pageURL, pgText("health detail test"), hs, bd).Scan(&id); err != nil {
		t.Fatalf("insert page %s: %v", pageURL, err)
	}
	return id
}

func getPageHealthDetail(t *testing.T, app *App, crawlID, pageID, userID pgtype.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/pages/"+pageID.String()+"/health", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	routeCtx.URLParams.Add("pageID", pageID.String())
	reqCtx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	reqCtx = withPrincipal(reqCtx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(reqCtx)
	rr := httptest.NewRecorder()
	app.handleGetCrawlPageHealthDetail(rr, req)
	return rr
}

func decodePageHealthDetailWire(t *testing.T, rr *httptest.ResponseRecorder) pageHealthDetailWire {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body as JSON object: %v (body %q)", err, rr.Body.String())
	}
	for _, key := range []string{"crawl_id", "page_id", "url", "health_score", "pillars"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("response missing contracted key %q (body %s)", key, rr.Body.String())
		}
	}
	var wire pageHealthDetailWire
	if err := json.Unmarshal(rr.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode health detail wire: %v", err)
	}
	return wire
}

func assertPageHealthDetailPillars(t *testing.T, wire pageHealthDetailWire) {
	t.Helper()
	want := []pageHealthDetailPillar{
		{ID: "seo", Score: 80, Buckets: []pageHealthDetailPillarBucket{{ID: "headings", Score: 90}, {ID: "meta_tags", Score: 70}}},
		{ID: "aeo", Score: 85, Buckets: []pageHealthDetailPillarBucket{{ID: "answerability", Score: 85}}},
		{ID: "pagespeed", Score: 60, Buckets: []pageHealthDetailPillarBucket{{ID: "cls", Score: 75}, {ID: "lcp", Score: 55}}},
	}
	if len(wire.Pillars) != len(want) {
		t.Fatalf("pillars len = %d, want %d (%+v)", len(wire.Pillars), len(want), wire.Pillars)
	}
	for i := range want {
		got, w := wire.Pillars[i], want[i]
		if got.ID != w.ID || got.Score != w.Score {
			t.Fatalf("pillar[%d] = {id:%q score:%d}, want {id:%q score:%d}", i, got.ID, got.Score, w.ID, w.Score)
		}
		if len(got.Buckets) != len(w.Buckets) {
			t.Fatalf("pillar %q buckets len = %d, want %d", got.ID, len(got.Buckets), len(w.Buckets))
		}
		for j := range w.Buckets {
			if got.Buckets[j] != w.Buckets[j] {
				t.Fatalf("pillar %q bucket[%d] = %+v, want %+v", got.ID, j, got.Buckets[j], w.Buckets[j])
			}
		}
	}
}

// A valid scored page returns persisted overall/pillar/bucket values exactly,
// with a stable body and immutable caching on a terminal crawl.
func TestPageHealthDetailScoredPageReturnsPersistedPillars(t *testing.T) {
	queries, pool, ctx, ownerID, _, projectID := setupPageHealthDetailTest(t)
	app := &App{DB: pool, Queries: queries}
	crawlID := insertPageHealthDetailCrawl(t, ctx, pool, projectID, "completed")

	score := int16(82)
	breakdown := pageHealthDetailTestBreakdown
	pageURL := "https://example.com/health-detail-scored"
	pageID := insertPageHealthDetailPage(t, ctx, pool, crawlID, pageURL, &score, breakdown)

	rr := getPageHealthDetail(t, app, crawlID, pageID, ownerID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	wire := decodePageHealthDetailWire(t, rr)
	if wire.CrawlID != crawlID.String() {
		t.Fatalf("crawl_id = %q, want %q", wire.CrawlID, crawlID.String())
	}
	if wire.PageID != pageID.String() {
		t.Fatalf("page_id = %q, want %q", wire.PageID, pageID.String())
	}
	if wire.URL != pageURL {
		t.Fatalf("url = %q, want %q", wire.URL, pageURL)
	}
	if wire.HealthScore != 82 {
		t.Fatalf("health_score = %d, want 82", wire.HealthScore)
	}
	assertPageHealthDetailPillars(t, wire)

	// Deterministic: a second identical request returns a byte-identical body.
	rr2 := getPageHealthDetail(t, app, crawlID, pageID, ownerID)
	if rr.Body.String() != rr2.Body.String() {
		t.Fatalf("repeat request body differs:\n%s\n%s", rr.Body.String(), rr2.Body.String())
	}

	if cc := rr.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Fatalf("terminal crawl Cache-Control = %q, want immutable", cc)
	}
}

// Old/unscored pages (NULL health_score, or scored but NULL health_breakdown)
// return 404.
func TestPageHealthDetailUnscoredPageReturns404(t *testing.T) {
	queries, pool, ctx, ownerID, _, projectID := setupPageHealthDetailTest(t)
	app := &App{DB: pool, Queries: queries}
	crawlID := insertPageHealthDetailCrawl(t, ctx, pool, projectID, "completed")

	nullScorePage := insertPageHealthDetailPage(t, ctx, pool, crawlID, "https://example.com/health-detail-null-score", nil, "")
	if rr := getPageHealthDetail(t, app, crawlID, nullScorePage, ownerID); rr.Code != http.StatusNotFound {
		t.Fatalf("NULL health_score status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}

	scoredNullBreakdown := int16(70)
	nullBreakdownPage := insertPageHealthDetailPage(t, ctx, pool, crawlID, "https://example.com/health-detail-null-breakdown", &scoredNullBreakdown, "")
	if rr := getPageHealthDetail(t, app, crawlID, nullBreakdownPage, ownerID); rr.Code != http.StatusNotFound {
		t.Fatalf("NULL health_breakdown status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}
}

// Tenancy boundary: a page from another crawl, or a user outside the org,
// returns 404.
func TestPageHealthDetailTenancyReturns404(t *testing.T) {
	queries, pool, ctx, ownerID, outsiderID, projectID := setupPageHealthDetailTest(t)
	app := &App{DB: pool, Queries: queries}
	crawlID := insertPageHealthDetailCrawl(t, ctx, pool, projectID, "completed")
	otherCrawlID := insertPageHealthDetailCrawl(t, ctx, pool, projectID, "completed")

	score := int16(82)
	breakdown := pageHealthDetailTestBreakdown
	pageID := insertPageHealthDetailPage(t, ctx, pool, crawlID, "https://example.com/health-detail-tenancy", &score, breakdown)

	if rr := getPageHealthDetail(t, app, otherCrawlID, pageID, ownerID); rr.Code != http.StatusNotFound {
		t.Fatalf("wrong crawl status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}
	if rr := getPageHealthDetail(t, app, crawlID, pageID, outsiderID); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d, want 404 (body %s)", rr.Code, rr.Body.String())
	}
}

// A live (non-terminal) crawl stays no-store even for a valid scored page.
func TestPageHealthDetailRunningCrawlUsesNoStore(t *testing.T) {
	queries, pool, ctx, ownerID, _, projectID := setupPageHealthDetailTest(t)
	app := &App{DB: pool, Queries: queries}
	crawlID := insertPageHealthDetailCrawl(t, ctx, pool, projectID, "running")

	score := int16(82)
	breakdown := pageHealthDetailTestBreakdown
	pageID := insertPageHealthDetailPage(t, ctx, pool, crawlID, "https://example.com/health-detail-running", &score, breakdown)

	rr := getPageHealthDetail(t, app, crawlID, pageID, ownerID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	wire := decodePageHealthDetailWire(t, rr)
	assertPageHealthDetailPillars(t, wire)
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("running crawl Cache-Control = %q, want no-store", cc)
	}
}
