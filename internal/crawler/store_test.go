package crawler

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	internaldb "github.com/ps-wizard/revserp/internal/db"
)

func TestStorePersistResult(t *testing.T) {
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

	crawlID, cleanup := createTestCrawl(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	result := CrawlResult{
		Job: CrawlJob{
			URL:   "https://example.com/",
			Depth: 0,
		},
		Fetch: FetchResult{
			FinalURL:     "https://example.com/",
			StatusCode:   200,
			ContentType:  "text/html; charset=utf-8",
			Body:         []byte("<html></html>"),
			ResponseTime: 150 * time.Millisecond,
			ResponseSize: 1234,
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
			Links: []ParsedLink{
				{
					TargetURL:  "https://example.com/about",
					AnchorText: "About",
					IsInternal: true,
					NoFollow:   false,
				},
				{
					TargetURL:  "https://example.com/about",
					AnchorText: "About",
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

	if err := store.PersistResult(ctx, crawlID, "https://example.com/", result); err != nil {
		t.Fatalf("persist result: %v", err)
	}

	var pageCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_pages WHERE crawl_id = $1`, crawlID).Scan(&pageCount); err != nil {
		t.Fatalf("count crawl pages: %v", err)
	}
	if pageCount != 1 {
		t.Fatalf("got %d crawl pages, want 1", pageCount)
	}

	var storedPage struct {
		URL                string
		Title              pgtype.Text
		MetaDescription    pgtype.Text
		H2Count            pgtype.Int4
		H3Count            pgtype.Int4
		InternalLinks      pgtype.Int4
		ExternalLinks      pgtype.Int4
		ResponseTimeMs     pgtype.Int4
		JavascriptRendered pgtype.Bool
	}
	if err := pool.QueryRow(ctx, `
		SELECT url, title, meta_description, h2_count, h3_count, internal_links, external_links, response_time_ms, javascript_rendered
		FROM crawl_pages
		WHERE crawl_id = $1
	`, crawlID).Scan(
		&storedPage.URL,
		&storedPage.Title,
		&storedPage.MetaDescription,
		&storedPage.H2Count,
		&storedPage.H3Count,
		&storedPage.InternalLinks,
		&storedPage.ExternalLinks,
		&storedPage.ResponseTimeMs,
		&storedPage.JavascriptRendered,
	); err != nil {
		t.Fatalf("load crawl page: %v", err)
	}

	if storedPage.URL != "https://example.com/" {
		t.Fatalf("got page url %q", storedPage.URL)
	}
	if storedPage.Title.String != "Home" {
		t.Fatalf("got page title %q", storedPage.Title.String)
	}
	if storedPage.MetaDescription.String != "Example home page" {
		t.Fatalf("got page meta description %q", storedPage.MetaDescription.String)
	}
	if storedPage.H2Count.Int32 != 2 {
		t.Fatalf("got h2 count %d", storedPage.H2Count.Int32)
	}
	if storedPage.H3Count.Int32 != 1 {
		t.Fatalf("got h3 count %d", storedPage.H3Count.Int32)
	}
	if storedPage.InternalLinks.Int32 != 2 {
		t.Fatalf("got internal links %d", storedPage.InternalLinks.Int32)
	}
	if storedPage.ExternalLinks.Int32 != 1 {
		t.Fatalf("got external links %d", storedPage.ExternalLinks.Int32)
	}
	if storedPage.ResponseTimeMs.Int32 != 150 {
		t.Fatalf("got response time %d", storedPage.ResponseTimeMs.Int32)
	}
	if !storedPage.JavascriptRendered.Valid || storedPage.JavascriptRendered.Bool {
		t.Fatalf("expected javascript_rendered to be false")
	}

	var linkCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_links WHERE crawl_id = $1`, crawlID).Scan(&linkCount); err != nil {
		t.Fatalf("count crawl links: %v", err)
	}
	if linkCount != 2 {
		t.Fatalf("got %d crawl links, want 2", linkCount)
	}
}

func TestStorePersistResultWithProcessErrorStillStoresPage(t *testing.T) {
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

	crawlID, cleanup := createTestCrawl(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	result := CrawlResult{
		Job: CrawlJob{
			URL:   "https://example.com/broken",
			Depth: 1,
		},
		Fetch: FetchResult{
			ResponseTime: 2 * time.Second,
		},
		ProcessErr: context.DeadlineExceeded,
	}

	if err := store.PersistResult(ctx, crawlID, "https://example.com/", result); err != nil {
		t.Fatalf("persist result with process error: %v", err)
	}

	var storedPage struct {
		URL            string
		Depth          pgtype.Int4
		StatusCode     pgtype.Int4
		ResponseTimeMs pgtype.Int4
		Title          pgtype.Text
		InternalLinks  pgtype.Int4
		ExternalLinks  pgtype.Int4
	}
	if err := pool.QueryRow(ctx, `
		SELECT url, depth, status_code, response_time_ms, title, internal_links, external_links
		FROM crawl_pages
		WHERE crawl_id = $1
	`, crawlID).Scan(
		&storedPage.URL,
		&storedPage.Depth,
		&storedPage.StatusCode,
		&storedPage.ResponseTimeMs,
		&storedPage.Title,
		&storedPage.InternalLinks,
		&storedPage.ExternalLinks,
	); err != nil {
		t.Fatalf("load crawl page: %v", err)
	}

	if storedPage.URL != "https://example.com/broken" {
		t.Fatalf("got page url %q", storedPage.URL)
	}
	if storedPage.Depth.Int32 != 1 {
		t.Fatalf("got page depth %d", storedPage.Depth.Int32)
	}
	if storedPage.StatusCode.Valid {
		t.Fatalf("expected status code to be null")
	}
	if storedPage.ResponseTimeMs.Int32 != 2000 {
		t.Fatalf("got response time %d", storedPage.ResponseTimeMs.Int32)
	}
	if storedPage.Title.Valid {
		t.Fatalf("expected title to be null")
	}
	if storedPage.InternalLinks.Int32 != 0 {
		t.Fatalf("got internal links %d", storedPage.InternalLinks.Int32)
	}
	if storedPage.ExternalLinks.Int32 != 0 {
		t.Fatalf("got external links %d", storedPage.ExternalLinks.Int32)
	}

	var linkCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_links WHERE crawl_id = $1`, crawlID).Scan(&linkCount); err != nil {
		t.Fatalf("count crawl links: %v", err)
	}
	if linkCount != 0 {
		t.Fatalf("got %d crawl links, want 0", linkCount)
	}
}

func loadCrawlerTestEnv(t *testing.T) {
	t.Helper()

	_, currentFilePath, _, ok := runtime.Caller(0)
	if !ok {
		return
	}

	repoRootPath := filepath.Clean(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
	envFilePath := filepath.Join(repoRootPath, ".env")
	_ = godotenv.Load(envFilePath)
}

func TestStoreMarksCrawlProgress(t *testing.T) {
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

	crawlID, cleanup := createTestCrawl(t, ctx, pool)
	defer cleanup()

	store := NewStore(pool)
	if err := store.MarkCrawlRunning(ctx, crawlID); err != nil {
		t.Fatalf("mark crawl running: %v", err)
	}

	if err := store.MarkCrawlCompleted(ctx, crawlID, 12, 10, 3); err != nil {
		t.Fatalf("mark crawl completed: %v", err)
	}

	var status string
	var urlsDiscovered int
	var urlsCrawled int
	var maxDepthReached int
	var startedAt pgtype.Timestamptz
	var completedAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT status, urls_discovered, urls_crawled, max_depth_reached, started_at, completed_at
		FROM crawls
		WHERE id = $1
	`, crawlID).Scan(&status, &urlsDiscovered, &urlsCrawled, &maxDepthReached, &startedAt, &completedAt); err != nil {
		t.Fatalf("load crawl: %v", err)
	}

	if status != "completed" {
		t.Fatalf("got status %q", status)
	}
	if urlsDiscovered != 12 || urlsCrawled != 10 || maxDepthReached != 3 {
		t.Fatalf("got counters discovered=%d crawled=%d maxDepth=%d", urlsDiscovered, urlsCrawled, maxDepthReached)
	}
	if !startedAt.Valid {
		t.Fatalf("expected started_at to be set")
	}
	if !completedAt.Valid {
		t.Fatalf("expected completed_at to be set")
	}
}

func TestMarkCrawlCompletedVerifiesRecordedWork(t *testing.T) {
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
		t.Fatal(err)
	}

	var sourceCrawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, started_at, completed_at) VALUES ($1, 'completed', now() - interval '2 hours', now() - interval '1 hour') RETURNING id`, projectID).Scan(&sourceCrawlID); err != nil {
		t.Fatal(err)
	}
	var sourcePageID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, 'https://example.com/a', 200) RETURNING id`, sourceCrawlID).Scan(&sourcePageID); err != nil {
		t.Fatal(err)
	}
	var sourceIssueID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawl_issues (crawl_id, crawl_page_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1, $2, 'https://example.com/a', 'seo', 'serp_metadata', 'missing_title', 'high', 'missing title', 'add title') RETURNING id`, sourceCrawlID, sourcePageID).Scan(&sourceIssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, 'https://example.com/a', 200)`, currentCrawlID); err != nil {
		t.Fatal(err)
	}

	var workItemID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue_work_items (project_id, subject_kind, subject_key, pillar, bucket, issue_type, source_crawl_issue_id, status) VALUES ($1, 'page', 'https://example.com/a', 'seo', 'serp_metadata', 'missing_title', $2, 'awaiting_verification') RETURNING id`, projectID, sourceIssueID).Scan(&workItemID); err != nil {
		t.Fatal(err)
	}
	var attemptID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue_work_attempts (work_item_id, source_crawl_id, status) VALUES ($1, $2, 'awaiting_verification') RETURNING id`, workItemID, sourceCrawlID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(pool)
	if err := store.MarkCrawlRunning(ctx, currentCrawlID); err != nil {
		t.Fatalf("mark crawl running: %v", err)
	}
	if err := store.MarkCrawlCompleted(ctx, currentCrawlID, 1, 1, 0); err != nil {
		t.Fatalf("mark crawl completed: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM issue_work_attempts WHERE id = $1`, attemptID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "fixed" {
		t.Fatalf("got status %q, want fixed", status)
	}

	var failedCrawlID, nextAttemptID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1, 'running') RETURNING id`, projectID).Scan(&failedCrawlID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO issue_work_attempts (work_item_id, source_crawl_id, status) VALUES ($1, $2, 'awaiting_verification') RETURNING id`, workItemID, currentCrawlID).Scan(&nextAttemptID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCrawlRunning(ctx, failedCrawlID); err != nil {
		t.Fatalf("mark second crawl running: %v", err)
	}
	if err := store.MarkCrawlFailed(ctx, failedCrawlID, 0, 0, 0); err != nil {
		t.Fatalf("mark crawl failed: %v", err)
	}
	var lockedAt pgtype.Timestamptz
	var verificationCrawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT locked_at, verification_crawl_id FROM issue_work_attempts WHERE id = $1`, nextAttemptID).Scan(&lockedAt, &verificationCrawlID); err != nil {
		t.Fatal(err)
	}
	if lockedAt.Valid || verificationCrawlID.Valid {
		t.Fatalf("failed crawl left attempt locked: locked=%v crawl=%v", lockedAt.Valid, verificationCrawlID.Valid)
	}
}

func createTestCrawl(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (pgtype.UUID, func()) {
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

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'Crawler Test Project', 'https://example.com/')
		RETURNING id
	`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}

	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO crawls (project_id, status)
		VALUES ($1, 'running')
		RETURNING id
	`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}

	cleanup := func() {
		if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
			t.Fatalf("cleanup organization: %v", err)
		}
	}

	return crawlID, cleanup
}
