package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/competitorgaps"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

func TestCompetitorGapNonBrandKeywords(t *testing.T) {
	lists := projectkeywords.KeywordLists{
		Combined: []projectkeywords.CombinedKeyword{
			{Keyword: "plumber", Kind: projectkeywords.ProjectKeywordKindNonBrand, Sources: []string{"user"}},
			{Keyword: "Acme", Kind: projectkeywords.ProjectKeywordKindBrand, Sources: []string{"user"}},
			{Keyword: "drain cleaning", Kind: projectkeywords.ProjectKeywordKindNonBrand, Sources: []string{"revserp"}},
		},
	}
	got := competitorGapNonBrandKeywords(lists)
	if len(got) != 2 || got[0] != "plumber" || got[1] != "drain cleaning" {
		t.Fatalf("non-brand keywords = %q, want [plumber drain cleaning]", got)
	}
	if got := competitorGapNonBrandKeywords(projectkeywords.KeywordLists{}); len(got) != 0 {
		t.Fatalf("empty lists = %q, want []", got)
	}
}

func TestCompetitorGapNonBrandKeywordsUserPrecedence(t *testing.T) {
	lists := projectkeywords.BuildProjectKeywordLists([]projectkeywords.StoredProjectKeyword{
		{ID: "1", Keyword: "emergency plumber", Normalized: "emergency plumber", Kind: projectkeywords.ProjectKeywordKindNonBrand, Source: projectkeywords.ProjectKeywordSourceUser},
		{ID: "2", Keyword: "Emergency Plumber", Normalized: "emergency plumber", Kind: projectkeywords.ProjectKeywordKindBrand, Source: projectkeywords.ProjectKeywordSourceRevserp},
	})
	got := competitorGapNonBrandKeywords(lists)
	if len(got) != 1 || got[0] != "emergency plumber" {
		t.Fatalf("precedence keywords = %q, want [emergency plumber]", got)
	}
}

func TestCompetitorGapKeywordCoverageNoGeoSeed(t *testing.T) {
	seeds := keywords.Cover([]keywords.Page{{URL: "https://you.example/", Title: "home", H1: "home"}}, []string{"plumber"}, "")
	for _, seed := range seeds {
		if seed.Geo {
			t.Fatalf("seed %q is geo, want no geo seeds without a primary location", seed.Keyword)
		}
	}
}

type competitorGapKeywordsTestResponse struct {
	Version         string `json:"version"`
	YourPages       int    `json:"your_pages"`
	KeywordCoverage struct {
		You  []keywords.Seed `json:"you"`
		Them []keywords.Seed `json:"them"`
	} `json:"keyword_coverage"`
}

func callCompetitorGap(t *testing.T, app *App, userID pgtype.UUID, crawlID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleGetCompetitorGap(rr, req)
	return rr
}

func TestHandleGetCompetitorGapKeywordCoverage(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)
	app := &App{DB: pool, Queries: queries}

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('gap-kw-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'gap-kw','https://you.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source) VALUES
		($1, 'plumber', 'plumber', 'non_brand', 'user'),
		($1, 'drain cleaning', 'drain cleaning', 'non_brand', 'revserp'),
		($1, 'emergency plumber', 'emergency plumber', 'non_brand', 'user'),
		($1, 'Emergency Plumber', 'emergency plumber', 'brand', 'revserp'),
		($1, 'Acme', 'acme', 'brand', 'user')
	`, projectID); err != nil {
		t.Fatal(err)
	}

	var parentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source, completed_at) VALUES ($1,'completed','manual', now() - interval '2 hour') RETURNING id`, projectID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, h1, status_code, content_type) VALUES
		($1, 'https://you.example/plumber', 'Emergency plumber services', 'Hire a plumber', 200, 'text/html')
	`, parentID); err != nil {
		t.Fatal(err)
	}
	var decoyID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source, completed_at) VALUES ($1,'completed','manual', now()) RETURNING id`, projectID).Scan(&decoyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, h1, status_code, content_type) VALUES
		($1, 'https://you.example/about', 'About our unrelated firm', 'Our story', 200, 'text/html')
	`, decoyID); err != nil {
		t.Fatal(err)
	}

	var competitorID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source, parent_crawl_id, completed_at) VALUES ($1,'completed','competitor',$2, now()) RETURNING id`, projectID, parentID).Scan(&competitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, h1, status_code, content_type) VALUES
		($1, 'https://them.example/drains', 'Drain cleaning experts', 'Drain cleaning pros', 200, 'text/html')
	`, competitorID); err != nil {
		t.Fatal(err)
	}

	rr := callCompetitorGap(t, app, userID, competitorID.String())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	var body competitorGapKeywordsTestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	if body.Version != competitorgaps.ReportVersion {
		t.Fatalf("version = %q, want %q (snapshot must not change)", body.Version, competitorgaps.ReportVersion)
	}
	if body.KeywordCoverage.You == nil || body.KeywordCoverage.Them == nil {
		t.Fatal("keyword_coverage sides must always be arrays, got null")
	}
	youByKeyword := map[string]keywords.Seed{}
	for _, seed := range body.KeywordCoverage.You {
		youByKeyword[seed.Keyword] = seed
	}
	themByKeyword := map[string]keywords.Seed{}
	for _, seed := range body.KeywordCoverage.Them {
		themByKeyword[seed.Keyword] = seed
	}
	for _, want := range []string{"plumber", "drain cleaning", "emergency plumber"} {
		if _, ok := youByKeyword[want]; !ok {
			t.Fatalf("you side missing %q: %#v", want, body.KeywordCoverage.You)
		}
		if _, ok := themByKeyword[want]; !ok {
			t.Fatalf("them side missing %q: %#v", want, body.KeywordCoverage.Them)
		}
	}
	for _, brand := range []string{"Acme", "acme"} {
		if _, ok := youByKeyword[brand]; ok {
			t.Fatalf("you side must exclude brand %q", brand)
		}
		if _, ok := themByKeyword[brand]; ok {
			t.Fatalf("them side must exclude brand %q", brand)
		}
	}
	for _, seed := range append(body.KeywordCoverage.You, body.KeywordCoverage.Them...) {
		if seed.Geo {
			t.Fatalf("seed %q is geo, gap coverage adds no geo seed", seed.Keyword)
		}
	}
	if youByKeyword["plumber"].State != keywords.StateLikelyTargeted {
		t.Fatalf("you plumber state = %q, want likely_targeted (actual parent, not decoy latest)", youByKeyword["plumber"].State)
	}
	if themByKeyword["drain cleaning"].State != keywords.StateLikelyTargeted {
		t.Fatalf("them drain cleaning state = %q, want likely_targeted", themByKeyword["drain cleaning"].State)
	}
	if themByKeyword["plumber"].State != keywords.StateNoLandingPage {
		t.Fatalf("them plumber state = %q, want no_landing_page", themByKeyword["plumber"].State)
	}
}

func TestHandleGetCompetitorGapKeywordCoverageEmptyList(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)
	app := &App{DB: pool, Queries: queries}

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('gap-kw-empty-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'gap-kw-empty','https://you-empty.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var parentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source, completed_at) VALUES ($1,'completed','manual', now()) RETURNING id`, projectID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	var competitorID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source, parent_crawl_id, completed_at) VALUES ($1,'completed','competitor',$2, now()) RETURNING id`, projectID, parentID).Scan(&competitorID); err != nil {
		t.Fatal(err)
	}

	rr := callCompetitorGap(t, app, userID, competitorID.String())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	coverage, ok := raw["keyword_coverage"].(map[string]any)
	if !ok {
		t.Fatalf("keyword_coverage missing: %s", rr.Body.String())
	}
	for _, side := range []string{"you", "them"} {
		seeds, ok := coverage[side].([]any)
		if !ok || seeds == nil {
			t.Fatalf("keyword_coverage.%s must be an array, got %v", side, coverage[side])
		}
		if len(seeds) != 0 {
			t.Fatalf("keyword_coverage.%s = %v, want []", side, seeds)
		}
	}
}
