package crawler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internaldb "github.com/ps-wizard/revserp/internal/db"
)

func TestLoadBaselineReturnsNilWithoutPreviousCrawl(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	currentCrawlID, cleanup := createTestCrawl(t, ctx, pool)
	defer cleanup()

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT project_id FROM crawls WHERE id = $1`, currentCrawlID).Scan(&projectID); err != nil {
		t.Fatalf("load project id: %v", err)
	}

	store := NewStore(pool)
	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline != nil {
		t.Fatalf("got baseline %+v, want nil", baseline)
	}
}

func TestLoadBaselineSkipsPagesWithoutValidators(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	projectID, baselineCrawlID, currentCrawlID, cleanup := createTestProjectWithTwoCrawls(t, ctx, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx, `
		INSERT INTO crawl_pages (crawl_id, url, status_code)
		VALUES ($1, 'https://example.com/no-validator', 200)
	`, baselineCrawlID); err != nil {
		t.Fatalf("seed page without validator: %v", err)
	}

	store := NewStore(pool)
	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline != nil {
		t.Fatalf("got baseline %+v, want nil", baseline)
	}
}

func TestLoadBaselineIndexesPagesByNormalizedURL(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	projectID, baselineCrawlID, currentCrawlID, cleanup := createTestProjectWithTwoCrawls(t, ctx, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx, `
		INSERT INTO crawl_pages (crawl_id, url, status_code, etag)
		VALUES ($1, 'https://example.com/etag-page', 200, '"v1"')
	`, baselineCrawlID); err != nil {
		t.Fatalf("seed etag page: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO crawl_pages (crawl_id, url, status_code, last_modified)
		VALUES ($1, 'https://example.com/last-modified-page', 200, 'Sat, 25 Jul 2026 21:36:16 GMT')
	`, baselineCrawlID); err != nil {
		t.Fatalf("seed last-modified page: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO crawl_pages (crawl_id, url, status_code, etag)
		VALUES ($1, 'https://example.com/not-found-page', 404, '"v1"')
	`, baselineCrawlID); err != nil {
		t.Fatalf("seed 404 page: %v", err)
	}

	store := NewStore(pool)
	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline == nil {
		t.Fatalf("got nil baseline, want non-nil")
	}
	if baseline.Len() != 2 {
		t.Fatalf("got baseline.Len() = %d, want 2", baseline.Len())
	}
	if baseline.CrawlID != baselineCrawlID {
		t.Fatalf("got baseline.CrawlID = %v, want %v", baseline.CrawlID, baselineCrawlID)
	}
}

func TestPersistReusedResultCopiesPageAndLinksForward(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	projectID, baselineCrawlID, currentCrawlID, cleanup := createTestProjectWithTwoCrawls(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	seedBaselineResult(t, ctx, store, baselineCrawlID)

	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline == nil {
		t.Fatalf("got nil baseline, want non-nil")
	}

	reusedResult := CrawlResult{
		Job: CrawlJob{
			URL:   "https://example.com/",
			Depth: 3,
		},
		Fetch: FetchResult{
			FinalURL:     "https://example.com/",
			StatusCode:   304,
			NotModified:  true,
			ResponseTime: 12 * time.Millisecond,
		},
	}
	if err := store.PersistReusedResult(ctx, currentCrawlID, baseline, reusedResult); err != nil {
		t.Fatalf("persist reused result: %v", err)
	}

	var pageCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_pages WHERE crawl_id = $1`, currentCrawlID).Scan(&pageCount); err != nil {
		t.Fatalf("count crawl pages: %v", err)
	}
	if pageCount != 1 {
		t.Fatalf("got %d crawl pages, want 1", pageCount)
	}

	var storedPage struct {
		Title           pgtype.Text
		MetaDescription pgtype.Text
		WordCount       pgtype.Int4
		VisibleText     pgtype.Text
		ContentType     pgtype.Text
		ResponseTimeMs  pgtype.Int4
		SizeBytes       pgtype.Int4
		Depth           pgtype.Int4
		Etag            pgtype.Text
	}
	if err := pool.QueryRow(ctx, `
		SELECT title, meta_description, word_count, visible_text, content_type, response_time_ms, size_bytes, depth, etag
		FROM crawl_pages
		WHERE crawl_id = $1
	`, currentCrawlID).Scan(
		&storedPage.Title,
		&storedPage.MetaDescription,
		&storedPage.WordCount,
		&storedPage.VisibleText,
		&storedPage.ContentType,
		&storedPage.ResponseTimeMs,
		&storedPage.SizeBytes,
		&storedPage.Depth,
		&storedPage.Etag,
	); err != nil {
		t.Fatalf("load crawl page: %v", err)
	}

	var baselinePageFacts struct {
		Title           pgtype.Text
		MetaDescription pgtype.Text
		WordCount       pgtype.Int4
		VisibleText     pgtype.Text
		ContentType     pgtype.Text
	}
	if err := pool.QueryRow(ctx, `
		SELECT title, meta_description, word_count, visible_text, content_type
		FROM crawl_pages
		WHERE crawl_id = $1
	`, baselineCrawlID).Scan(
		&baselinePageFacts.Title,
		&baselinePageFacts.MetaDescription,
		&baselinePageFacts.WordCount,
		&baselinePageFacts.VisibleText,
		&baselinePageFacts.ContentType,
	); err != nil {
		t.Fatalf("load baseline crawl page: %v", err)
	}

	if storedPage.Title.String != baselinePageFacts.Title.String {
		t.Fatalf("got title %q, want baseline title %q", storedPage.Title.String, baselinePageFacts.Title.String)
	}
	if storedPage.MetaDescription.String != baselinePageFacts.MetaDescription.String {
		t.Fatalf("got meta description %q, want baseline meta description %q", storedPage.MetaDescription.String, baselinePageFacts.MetaDescription.String)
	}
	if storedPage.WordCount.Int32 != baselinePageFacts.WordCount.Int32 {
		t.Fatalf("got word count %d, want baseline word count %d", storedPage.WordCount.Int32, baselinePageFacts.WordCount.Int32)
	}
	if storedPage.VisibleText.String != baselinePageFacts.VisibleText.String {
		t.Fatalf("got visible text %q, want baseline visible text %q", storedPage.VisibleText.String, baselinePageFacts.VisibleText.String)
	}
	if storedPage.ContentType.String != baselinePageFacts.ContentType.String {
		t.Fatalf("got content type %q, want baseline content type %q", storedPage.ContentType.String, baselinePageFacts.ContentType.String)
	}

	if storedPage.ResponseTimeMs.Int32 != 1500 {
		t.Fatalf("BUG: got response_time_ms %d, want baseline's 1500 (a 304 is bodyless and fast; recording its 12ms timing would silently inflate PageSpeed scores)", storedPage.ResponseTimeMs.Int32)
	}
	if storedPage.SizeBytes.Int32 != 98765 {
		t.Fatalf("got size_bytes %d, want baseline's 98765", storedPage.SizeBytes.Int32)
	}
	if storedPage.Depth.Int32 != 3 {
		t.Fatalf("got depth %d, want current crawl's 3", storedPage.Depth.Int32)
	}
	if storedPage.Etag.String != `"v1"` {
		t.Fatalf(`got etag %q, want baseline's "v1"`, storedPage.Etag.String)
	}

	var baselineLinkCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM crawl_links WHERE crawl_id = $1 AND source_url = 'https://example.com/'
	`, baselineCrawlID).Scan(&baselineLinkCount); err != nil {
		t.Fatalf("count baseline crawl links: %v", err)
	}

	var currentLinkCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_links WHERE crawl_id = $1`, currentCrawlID).Scan(&currentLinkCount); err != nil {
		t.Fatalf("count current crawl links: %v", err)
	}
	if currentLinkCount != baselineLinkCount {
		t.Fatalf("got %d crawl links for current crawl, want %d (baseline's count)", currentLinkCount, baselineLinkCount)
	}
}

func TestPersistReusedResultAppliesFreshETag(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	projectID, baselineCrawlID, currentCrawlID, cleanup := createTestProjectWithTwoCrawls(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	seedBaselineResult(t, ctx, store, baselineCrawlID)

	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline == nil {
		t.Fatalf("got nil baseline, want non-nil")
	}

	reusedResult := CrawlResult{
		Job: CrawlJob{
			URL:   "https://example.com/",
			Depth: 3,
		},
		Fetch: FetchResult{
			FinalURL:     "https://example.com/",
			StatusCode:   304,
			NotModified:  true,
			ResponseTime: 12 * time.Millisecond,
			ETag:         `"v2"`,
		},
	}
	if err := store.PersistReusedResult(ctx, currentCrawlID, baseline, reusedResult); err != nil {
		t.Fatalf("persist reused result: %v", err)
	}

	var etag pgtype.Text
	if err := pool.QueryRow(ctx, `SELECT etag FROM crawl_pages WHERE crawl_id = $1`, currentCrawlID).Scan(&etag); err != nil {
		t.Fatalf("load crawl page etag: %v", err)
	}
	if etag.String != `"v2"` {
		t.Fatalf(`got etag %q, want fresh "v2"`, etag.String)
	}
}

func TestPersistReusedResultIsIdempotent(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	projectID, baselineCrawlID, currentCrawlID, cleanup := createTestProjectWithTwoCrawls(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	seedBaselineResult(t, ctx, store, baselineCrawlID)

	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline == nil {
		t.Fatalf("got nil baseline, want non-nil")
	}

	reusedResult := CrawlResult{
		Job: CrawlJob{
			URL:   "https://example.com/",
			Depth: 3,
		},
		Fetch: FetchResult{
			FinalURL:     "https://example.com/",
			StatusCode:   304,
			NotModified:  true,
			ResponseTime: 12 * time.Millisecond,
		},
	}

	if err := store.PersistReusedResult(ctx, currentCrawlID, baseline, reusedResult); err != nil {
		t.Fatalf("persist reused result (first call): %v", err)
	}
	if err := store.PersistReusedResult(ctx, currentCrawlID, baseline, reusedResult); err != nil {
		t.Fatalf("persist reused result (second call): %v", err)
	}

	var pageCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_pages WHERE crawl_id = $1`, currentCrawlID).Scan(&pageCount); err != nil {
		t.Fatalf("count crawl pages: %v", err)
	}
	if pageCount != 1 {
		t.Fatalf("got %d crawl pages after two calls, want 1", pageCount)
	}
}

func TestListBaselineInternalTargetsReturnsInternalLinksOnly(t *testing.T) {
	loadCrawlerTestEnv(t)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()

	projectID, baselineCrawlID, currentCrawlID, cleanup := createTestProjectWithTwoCrawls(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	seedBaselineResult(t, ctx, store, baselineCrawlID)

	baseline, err := store.LoadBaseline(ctx, projectID, currentCrawlID)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if baseline == nil {
		t.Fatalf("got nil baseline, want non-nil")
	}

	targetURLs := baseline.internalTargets("https://example.com/")
	if len(targetURLs) != 2 {
		t.Fatalf("got %d internal targets, want 2: %v", len(targetURLs), targetURLs)
	}
	wantTargets := map[string]bool{
		"https://example.com/about":   false,
		"https://example.com/contact": false,
	}
	for _, targetURL := range targetURLs {
		if targetURL == "https://vercel.com/" {
			t.Fatalf("got external target %q in internal targets", targetURL)
		}
		if _, expected := wantTargets[targetURL]; !expected {
			t.Fatalf("got unexpected internal target %q", targetURL)
		}
		wantTargets[targetURL] = true
	}
	for targetURL, seen := range wantTargets {
		if !seen {
			t.Fatalf("missing expected internal target %q", targetURL)
		}
	}

	unknownTargets := baseline.internalTargets("https://example.com/unknown")
	if unknownTargets != nil {
		t.Fatalf("got %v for unknown url, want nil", unknownTargets)
	}
}

// seedBaselineResult persists a realistic CrawlResult into the baseline crawl
// via store.PersistResult, giving TEST D/E/F/G a page with cache validators,
// timing/size facts, and both an internal and external link to copy forward.
func seedBaselineResult(t *testing.T, ctx context.Context, store *Store, baselineCrawlID pgtype.UUID) {
	t.Helper()

	result := CrawlResult{
		Job: CrawlJob{
			URL:   "https://example.com/",
			Depth: 0,
		},
		Fetch: FetchResult{
			FinalURL:     "https://example.com/",
			StatusCode:   200,
			ContentType:  "text/html; charset=utf-8",
			ETag:         `"v1"`,
			Body:         []byte("<html></html>"),
			ResponseTime: 1500 * time.Millisecond,
			ResponseSize: 98765,
		},
		ParsedPage: &ParsedPage{
			URL:             "https://example.com/",
			Title:           "Home",
			MetaDescription: "Example home page",
			CanonicalURL:    "https://example.com/",
			Lang:            "en",
			Robots:          "index,follow",
			H1:              "Welcome",
			H1Count:         1,
			H2Headings:      []string{"Overview", "Features"},
			H3Headings:      []string{"Fast"},
			VisibleText:     "Welcome to the example home page.",
			Links: []ParsedLink{
				{
					TargetURL:  "https://example.com/about",
					AnchorText: "About",
					IsInternal: true,
					NoFollow:   false,
				},
				{
					TargetURL:  "https://example.com/contact",
					AnchorText: "Contact",
					IsInternal: true,
					NoFollow:   false,
				},
				{
					TargetURL:  "https://vercel.com/",
					AnchorText: "Vercel",
					IsInternal: false,
					NoFollow:   true,
				},
			},
		},
	}

	if err := store.PersistResult(ctx, baselineCrawlID, "https://example.com/", result); err != nil {
		t.Fatalf("seed baseline result: %v", err)
	}
}

// createTestProjectWithTwoCrawls sets up one project with a completed baseline
// crawl and a running current crawl, so incremental-crawl tests can exercise
// LoadBaseline/PersistReusedResult across two crawls of the same project.
// Cleanup deletes the organization, cascading to everything created here —
// mirroring createTestCrawl.
func createTestProjectWithTwoCrawls(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (projectID pgtype.UUID, baselineCrawlID pgtype.UUID, currentCrawlID pgtype.UUID, cleanup func()) {
	t.Helper()

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (auth_provider, auth_subject, email, name)
		VALUES ('test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com', 'Crawler Test User')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}

	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO organizations (name)
		VALUES ('Crawler Test Org')
		RETURNING id
	`).Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO organization_members (org_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, orgID, userID); err != nil {
		t.Fatalf("create organization member: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'Crawler Test Project', 'https://example.com/')
		RETURNING id
	`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO crawls (project_id, status, completed_at)
		VALUES ($1, 'completed', now())
		RETURNING id
	`, projectID).Scan(&baselineCrawlID); err != nil {
		t.Fatalf("create baseline crawl: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO crawls (project_id, status)
		VALUES ($1, 'running')
		RETURNING id
	`, projectID).Scan(&currentCrawlID); err != nil {
		t.Fatalf("create current crawl: %v", err)
	}

	cleanup = func() {
		if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
			t.Fatalf("cleanup organization: %v", err)
		}
	}

	return projectID, baselineCrawlID, currentCrawlID, cleanup
}
