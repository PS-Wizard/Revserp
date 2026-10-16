package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Comparison baseline unit checks run without a database or application .env.

func TestComparisonBaselineRouteRegistered(t *testing.T) {
	app := &App{Config: config.Config{}}
	routes := map[string]bool{}
	if err := chi.Walk(app.Router().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if !routes["GET /crawls/{crawlID}/comparison-baseline"] {
		t.Errorf("GET /crawls/{crawlID}/comparison-baseline is not registered")
	}
}

func TestComparisonBaselineOverrideRequested(t *testing.T) {
	for raw, want := range map[string]bool{
		"/":                             false,
		"/?other=1":                     false,
		"/?baseline_crawl_id=":          false,
		"/?baseline_crawl_id=%20%20%20": false,
		"/?baseline_crawl_id=00000000-0000-0000-0000-000000000001": true,
	} {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		if got := comparisonBaselineOverrideRequested(req); got != want {
			t.Errorf("comparisonBaselineOverrideRequested(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestIsComparableComparisonSource(t *testing.T) {
	for _, source := range []string{"manual", "auto", "mcp"} {
		if !isComparableComparisonSource(source) {
			t.Errorf("isComparableComparisonSource(%q) = false, want true", source)
		}
	}
	for _, source := range []string{"", "competitor", "location", "MANUAL"} {
		if isComparableComparisonSource(source) {
			t.Errorf("isComparableComparisonSource(%q) = true, want false", source)
		}
	}
}

func TestBuildComparisonBaselineResponseNull(t *testing.T) {
	var currentID pgtype.UUID
	if err := currentID.Scan("00000000-0000-0000-0000-000000000009"); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(buildComparisonBaselineResponse(currentID, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["current_crawl_id"] != currentID.String() {
		t.Errorf("current_crawl_id = %v, want %s", decoded["current_crawl_id"], currentID.String())
	}
	for _, key := range []string{"baseline_crawl", "baseline_breakdown"} {
		value, present := decoded[key]
		if !present {
			t.Errorf("response is missing %q", key)
		} else if value != nil {
			t.Errorf("%s = %v, want null", key, value)
		}
	}
}

func TestBuildComparisonBaselineResponseMetadata(t *testing.T) {
	var currentID, baselineID, projectID pgtype.UUID
	if err := currentID.Scan("00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := baselineID.Scan("00000000-0000-0000-0000-000000000002"); err != nil {
		t.Fatal(err)
	}
	if err := projectID.Scan("00000000-0000-0000-0000-000000000003"); err != nil {
		t.Fatal(err)
	}
	completed := pgtype.Timestamptz{Time: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Valid: true}
	breakdown := json.RawMessage(`{"crawl_id":"x","overall_score":90}`)
	got := buildComparisonBaselineResponse(currentID, &sqlc.GetCrawlByIDForUserRow{
		ID: baselineID, ProjectID: projectID, Status: "completed", CompletedAt: completed,
	}, breakdown)
	if got.CurrentCrawlID != currentID.String() {
		t.Errorf("CurrentCrawlID = %q, want %q", got.CurrentCrawlID, currentID.String())
	}
	if got.BaselineCrawl == nil {
		t.Fatal("BaselineCrawl is nil")
	}
	if got.BaselineCrawl.ID != baselineID.String() || got.BaselineCrawl.ProjectID != projectID.String() || got.BaselineCrawl.Status != "completed" {
		t.Errorf("baseline metadata = %+v", got.BaselineCrawl)
	}
	if got.BaselineCrawl.StartedAt != nil {
		t.Errorf("StartedAt = %v, want null for absent timestamp", *got.BaselineCrawl.StartedAt)
	}
	if got.BaselineCrawl.CompletedAt == nil || *got.BaselineCrawl.CompletedAt != "2026-10-01T12:00:00Z" {
		t.Errorf("CompletedAt = %v, want 2026-10-01T12:00:00Z", got.BaselineCrawl.CompletedAt)
	}
	if string(got.BaselineBreakdown) != string(breakdown) {
		t.Errorf("breakdown = %s, want %s", got.BaselineBreakdown, breakdown)
	}
}

func TestValidateComparisonBaselineBreakdown(t *testing.T) {
	raw := []byte(`{"crawl_id":"x","overall_score":90,"pillars":[]}`)
	validated, err := validateComparisonBaselineBreakdown(raw)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if string(validated) != string(raw) {
		t.Errorf("validated = %s, want passthrough of %s", validated, raw)
	}
	if _, err := validateComparisonBaselineBreakdown([]byte(`{broken`)); err == nil {
		t.Errorf("malformed snapshot should error honestly")
	}
	if _, err := validateComparisonBaselineBreakdown(nil); err == nil {
		t.Errorf("empty snapshot should error")
	}
}

func TestHandleGetCrawlComparisonBaselineRejects(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")

	call := func(rawURL, crawlID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, rawURL, nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("crawlID", crawlID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
		rr := httptest.NewRecorder()
		app.handleGetCrawlComparisonBaseline(rr, req.WithContext(ctx))
		return rr
	}

	if rr := call("/", "not-a-uuid"); rr.Code != http.StatusBadRequest {
		t.Errorf("bad crawl id status = %d, want 400", rr.Code)
	}
	validID := "00000000-0000-0000-0000-000000000002"
	rr := call("/?baseline_crawl_id="+validID, validID)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("override status = %d, want 400", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "baseline_crawl_id") {
		t.Errorf("override body should name baseline_crawl_id, got %q", body)
	}
}

// The regression checks below need a real Postgres. They connect only to the
// disposable database named by ISSUE_DIFF_TEST_DATABASE_URL, never to
// DATABASE_URL and never by loading an application .env, and skip when absent.

func newIssueDiffTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("ISSUE_DIFF_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ISSUE_DIFF_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("issue diff test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"public.crawls", "public.crawl_pages", "public.crawl_issues", "public.crawl_score_breakdowns"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("table %s is not migrated in the test database", table)
		}
	}
	return sqlc.New(pool), pool, ctx
}

func TestIssueTypeDiffCompareURLsRegression(t *testing.T) {
	queries, pool, ctx := newIssueDiffTestPool(t)
	name := "issue-diff-test"
	var orgID, ownerID, projectID, baselineID, currentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('issue-diff-test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, ownerID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'issue-diff-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now() - interval '2 hour', now() - interval '3 hour') RETURNING id`, projectID).Scan(&baselineID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now() - interval '1 hour') RETURNING id`, projectID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}

	const pageA, pageB = "https://example.com/a", "https://example.com/b"
	const pageC = "https://example.com/c"
	// Baseline: page A misses both its title and its meta description in the
	// same serp_metadata bucket; page B misses its title.
	for _, issue := range [][2]string{{pageA, "missing_title"}, {pageA, "missing_meta_description"}, {pageB, "missing_title"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1,$2,'seo','serp_metadata',$3,'high','msg','det')`, baselineID, issue[0], issue[1]); err != nil {
			t.Fatal(err)
		}
	}
	// Current crawl re-observed page A (healthy) and fixed only its title, so
	// the description issue persists; page C is brand new. Page B was never
	// observed, so its title must stay unverified instead of resolved.
	for _, page := range []string{pageA, pageC} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,$2,200)`, currentID, page); err != nil {
			t.Fatal(err)
		}
	}
	for _, issue := range [][2]string{{pageA, "missing_meta_description"}, {pageC, "missing_title"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1,$2,'seo','serp_metadata',$3,'high','msg','det')`, currentID, issue[0], issue[1]); err != nil {
			t.Fatal(err)
		}
	}

	changeByURL := func(issueType string) map[string]string {
		t.Helper()
		rows, err := queries.ListCompareCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.ListCompareCrawlIssueURLsByTypeForCrawlByUserParams{
			CrawlID: baselineID, CrawlID_2: currentID, UserID: ownerID,
			Pillar: "seo", Bucket: "serp_metadata", IssueType: issueType, Column7: "", Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, row := range rows {
			got[row.Url] = row.ChangeType
		}
		return got
	}

	// Missing title disappears on its own while the same page keeps its
	// missing description: per-issue-type diffs, never bucket-wide verdicts.
	if got := changeByURL("missing_title"); got[pageA] != "no_longer_detected" || got[pageC] != "new" {
		t.Errorf("missing_title changes = %v, want a=no_longer_detected c=new", got)
	}
	if got := changeByURL("missing_meta_description"); got[pageA] != "unchanged" {
		t.Errorf("missing_meta_description changes = %v, want a=unchanged", got)
	}
	// The unchecked page is not evidence of a fix.
	if got := changeByURL("missing_title"); got[pageB] != "not_verified" {
		t.Errorf("missing_title changes = %v, want b=not_verified", got)
	}
	total, err := queries.CountCompareCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.CountCompareCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID: baselineID, CrawlID_2: currentID, UserID: ownerID,
		Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Column7: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("missing_title total = %d, want 3 (resolved, new, unverified)", total)
	}
}

func TestHandleGetCrawlComparisonBaseline(t *testing.T) {
	queries, pool, ctx := newIssueDiffTestPool(t)
	name := "comparison-baseline-test"
	var orgID, ownerID, outsiderID, projectID, firstID, secondID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })
	newUser := func(suffix string) pgtype.UUID {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('comparison-baseline-test', $1, $2) RETURNING id`, name+suffix, name+suffix+"@example.com").Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		return id
	}
	ownerID, outsiderID = newUser("owner"), newUser("outsider")
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'comparison-baseline-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now() - interval '2 hour', now() - interval '3 hour') RETURNING id`, projectID).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now() - interval '1 hour') RETURNING id`, projectID).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	snapshot := `{"crawl_id":"` + firstID.String() + `","overall_score":88,"pillars":[]}`
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_score_breakdowns (crawl_id, scoring_version, breakdown_json) VALUES ($1,'v1',$2::jsonb)`, firstID, snapshot); err != nil {
		t.Fatal(err)
	}

	app := &App{Queries: queries, DB: pool}
	call := func(userID pgtype.UUID, crawlID pgtype.UUID, rawURL string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, rawURL, nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("crawlID", crawlID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
		rr := httptest.NewRecorder()
		app.handleGetCrawlComparisonBaseline(rr, req.WithContext(ctx))
		return rr
	}
	decode := func(rr *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rr.Body.String(), err)
		}
		return body
	}

	// First crawl: null baseline, never itself as baseline.
	rr := call(ownerID, firstID, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("first crawl status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if body := decode(rr); body["baseline_crawl"] != nil || body["baseline_breakdown"] != nil || body["current_crawl_id"] != firstID.String() {
		t.Errorf("first crawl body = %v, want null baseline", body)
	}

	// Second crawl: predecessor metadata plus the full stored snapshot.
	rr = call(ownerID, secondID, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("second crawl status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := decode(rr)
	baseline, ok := body["baseline_crawl"].(map[string]any)
	if !ok {
		t.Fatalf("baseline_crawl = %v, want object", body["baseline_crawl"])
	}
	if baseline["id"] != firstID.String() || baseline["project_id"] != projectID.String() || baseline["status"] != "completed" {
		t.Errorf("baseline metadata = %v", baseline)
	}
	breakdown, ok := body["baseline_breakdown"].(map[string]any)
	if !ok || breakdown["overall_score"] != float64(88) {
		t.Errorf("baseline_breakdown = %v, want stored snapshot", body["baseline_breakdown"])
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rr.Header().Get("Cache-Control"))
	}

	// Scope guard: outsiders cannot resolve either crawl.
	if rr := call(outsiderID, secondID, "/"); rr.Code != http.StatusNotFound {
		t.Errorf("outsider status = %d, want 404", rr.Code)
	}
}

// TestHandleGetCrawlComparisonBaselineExcludedIssueTypes is the regression
// for exclusions hiding as fixes: the current crawl excludes /blogs/*, so
// the two previous low_internal_links_in issues under /blogs/* surface as
// excluded (never checked), never as confirmed fixes. URLs the current
// crawl actually saw, or that match no exclusion, are never excluded.
func TestHandleGetCrawlComparisonBaselineExcludedIssueTypes(t *testing.T) {
	queries, pool, ctx := newIssueDiffTestPool(t)
	name := "comparison-excluded-test"
	var orgID, ownerID, projectID, baselineID, currentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('comparison-excluded-test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, ownerID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'comparison-excluded-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now() - interval '2 hour', now() - interval '3 hour') RETURNING id`, projectID).Scan(&baselineID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now() - interval '1 hour') RETURNING id`, projectID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE crawls SET config_snapshot = '{"excluded_url_patterns":["/blogs/*"]}'::jsonb WHERE id = $1`, currentID); err != nil {
		t.Fatal(err)
	}

	const excludedA, excludedB = "https://example.com/blogs/category/paid-media/", "https://example.com/blogs/page/2/"
	const seen, other = "https://example.com/seen", "https://example.com/about"
	for _, page := range []string{excludedA, excludedB, seen, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1,$2,'seo','internal_linking','low_internal_links_in','low','msg','det')`, baselineID, page); err != nil {
			t.Fatal(err)
		}
	}
	// The current crawl observed /seen, so its previous issue is a candidate
	// for neither exclusion nor a fix verdict here: the candidate query
	// drops URLs with a current crawl_pages record.
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,$2,200)`, currentID, seen); err != nil {
		t.Fatal(err)
	}

	app := &App{Queries: queries, DB: pool}
	call := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("crawlID", currentID.String())
		reqCtx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		reqCtx = withPrincipal(reqCtx, Principal{User: sqlc.User{ID: ownerID}})
		rr := httptest.NewRecorder()
		app.handleGetCrawlComparisonBaseline(rr, req.WithContext(reqCtx))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rr.Body.String(), err)
		}
		return body
	}

	body := call()
	rawo, present := body["excluded_issue_types"]
	if !present {
		t.Fatalf("response omits excluded_issue_types, body=%v", body)
	}
	entries, ok := rawo.([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("excluded_issue_types = %v, want one entry", rawo)
	}
	entry, ok := entries[0].(map[string]any)
	if !ok || entry["pillar"] != "seo" || entry["bucket"] != "internal_linking" || entry["issue_type"] != "low_internal_links_in" || entry["excluded_url_count"] != float64(2) {
		t.Errorf("excluded entry = %v, want seo/internal_linking/low_internal_links_in count 2", entries[0])
	}

	// Without exclusions the optional field stays omitted: existing shape
	// unchanged, no drift for older clients.
	if _, err := pool.Exec(ctx, `UPDATE crawls SET config_snapshot = '{}'::jsonb WHERE id = $1`, currentID); err != nil {
		t.Fatal(err)
	}
	if body := call(); body["excluded_issue_types"] != nil {
		t.Errorf("excluded_issue_types = %v, want omitted without exclusions", body["excluded_issue_types"])
	}
}

// TestIssueTypeDiffCompareURLsIncludeExcludedUnion is the regression for the
// fixed-card union: with include_excluded the resolved list holds real fixes
// plus exclusion-skipped URLs as change_type excluded, with baseline evidence.
// Legacy calls without the URL set stay identical, and count/list paginate
// the same union. Needs the disposable ISSUE_DIFF_TEST_DATABASE_URL.
func TestIssueTypeDiffCompareURLsIncludeExcludedUnion(t *testing.T) {
	queries, pool, ctx := newIssueDiffTestPool(t)
	name := "issue-diff-excluded-union-test"
	var orgID, ownerID, projectID, baselineID, currentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('issue-diff-excluded-union-test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, ownerID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'issue-diff-excluded-union-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now() - interval '2 hour', now() - interval '3 hour') RETURNING id`, projectID).Scan(&baselineID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now() - interval '1 hour') RETURNING id`, projectID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"excluded_url_patterns":["/blogs/*"]}`)
	if _, err := pool.Exec(ctx, `UPDATE crawls SET config_snapshot = $1::jsonb WHERE id = $2`, snapshot, currentID); err != nil {
		t.Fatal(err)
	}

	const fixed, gone = "https://example.com/fixed", "https://example.com/gone"
	const excludedA, excludedB = "https://example.com/blogs/a", "https://example.com/blogs/b"
	for _, page := range []string{fixed, gone, excludedA, excludedB} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1,$2,'seo','serp_metadata','missing_title','high','msg','det')`, baselineID, page); err != nil {
			t.Fatal(err)
		}
	}
	// Only the fixed page was re-observed healthy; the rest have no current pages.
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,$2,200)`, currentID, fixed); err != nil {
		t.Fatal(err)
	}

	excludedURLs, err := loadComparisonExcludedURLs(ctx, queries, ownerID, baselineID, currentID, "seo", "serp_metadata", "missing_title", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(excludedURLs) != 2 || excludedURLs[0] != excludedA || excludedURLs[1] != excludedB {
		t.Fatalf("excluded URLs = %v, want [%s %s]", excludedURLs, excludedA, excludedB)
	}

	count := func(changeType string, urls []string) int64 {
		t.Helper()
		total, err := queries.CountCompareCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.CountCompareCrawlIssueURLsByTypeForCrawlByUserParams{
			CrawlID: baselineID, CrawlID_2: currentID, UserID: ownerID,
			Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Column7: changeType,
			ExcludedUrls: urls,
		})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	list := func(changeType string, urls []string, limit, offset int32) []sqlc.ListCompareCrawlIssueURLsByTypeForCrawlByUserRow {
		t.Helper()
		rows, err := queries.ListCompareCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.ListCompareCrawlIssueURLsByTypeForCrawlByUserParams{
			CrawlID: baselineID, CrawlID_2: currentID, UserID: ownerID,
			Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Column7: changeType,
			Limit: limit, Offset: offset, ExcludedUrls: urls,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	// Legacy call without the URL set: one real fix, no excluded rows.
	if total := count("resolved", nil); total != 1 {
		t.Errorf("legacy resolved count = %d, want 1", total)
	}
	if rows := list("resolved", nil, 10, 0); len(rows) != 1 || rows[0].ChangeType != "no_longer_detected" || rows[0].Url != fixed {
		t.Errorf("legacy resolved rows = %+v, want the single fix", rows)
	}

	// Union: the fix plus both excluded URLs, ordered by URL for pagination.
	if total := count("resolved", excludedURLs); total != 3 {
		t.Errorf("union resolved count = %d, want 3", total)
	}
	rows := list("resolved", excludedURLs, 10, 0)
	if len(rows) != 3 {
		t.Fatalf("union resolved rows = %+v, want 3", rows)
	}
	want := map[string]string{excludedA: "excluded", excludedB: "excluded", fixed: "no_longer_detected"}
	for i, row := range rows {
		if want[row.Url] != row.ChangeType {
			t.Errorf("rows[%d] = %s/%s, want %s", i, row.Url, row.ChangeType, want[row.Url])
		}
		if !row.BaselineSeverity.Valid {
			t.Errorf("rows[%d] %s misses baseline evidence", i, row.Url)
		}
		if i > 0 && rows[i-1].Url >= row.Url {
			t.Errorf("rows out of order: %s before %s", rows[i-1].Url, row.Url)
		}
	}

	// Pagination covers the full union, matching the count.
	first := list("resolved", excludedURLs, 2, 0)
	second := list("resolved", excludedURLs, 2, 2)
	if len(first) != 2 || len(second) != 1 {
		t.Fatalf("pages = %d/%d rows, want 2/1", len(first), len(second))
	}
	for i, row := range append(first, second...) {
		if row.Url != rows[i].Url {
			t.Errorf("page row %d = %s, want %s", i, row.Url, rows[i].Url)
		}
	}
}
