package crawler

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// The resolve step is raw SQL with its own URL normalization, so it can only be
// verified against a real Postgres.
func newResolveTestStore(t *testing.T) (*Store, *pgxpool.Pool, pgtype.UUID, context.Context) {
	t.Helper()
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
	t.Cleanup(pool.Close)

	crawlID, cleanup := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanup)

	return NewStore(pool), pool, crawlID, ctx
}

func insertResolveTestPage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, crawlID pgtype.UUID, url string, statusCode int) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, $2, $3)`,
		crawlID, url, statusCode)
	if err != nil {
		t.Fatalf("insert page %q: %v", url, err)
	}
}

func insertResolveTestLink(t *testing.T, ctx context.Context, pool *pgxpool.Pool, crawlID pgtype.UUID, sourceURL, targetURL string, isInternal bool) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal) VALUES ($1, $2, $3, $4)`,
		crawlID, sourceURL, targetURL, isInternal)
	if err != nil {
		t.Fatalf("insert link %q -> %q: %v", sourceURL, targetURL, err)
	}
}

func targetStatusFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, crawlID pgtype.UUID, targetURL string) (int32, bool) {
	t.Helper()
	var status pgtype.Int4
	err := pool.QueryRow(ctx,
		`SELECT target_status FROM crawl_links WHERE crawl_id = $1 AND target_url = $2`,
		crawlID, targetURL).Scan(&status)
	if err != nil {
		t.Fatalf("read target_status for %q: %v", targetURL, err)
	}
	return status.Int32, status.Valid
}

func TestResolveInternalLinkTargetStatuses(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)

	insertResolveTestPage(t, ctx, pool, crawlID, "https://example.com/", 200)
	insertResolveTestPage(t, ctx, pool, crawlID, "https://example.com/gone", 404)
	insertResolveTestPage(t, ctx, pool, crawlID, "https://example.com/moved", 301)

	// Exact match, and the three normalization cases the site graph also handles:
	// trailing slash, fragment, and uppercase host.
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/gone", true)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/moved/", true)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/gone#section", true)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://EXAMPLE.com/gone", true)
	// A query string makes a distinct URL, exactly as normalizeGraphURL treats it,
	// so this must not resolve against the bare path.
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/gone?x=1", true)
	// An external link and a link to an uncrawled URL must both stay NULL.
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://other.test/page", false)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/never-crawled", true)

	rows, err := store.ResolveInternalLinkTargetStatuses(ctx, crawlID)
	if err != nil {
		t.Fatalf("ResolveInternalLinkTargetStatuses: %v", err)
	}
	if rows == 0 {
		t.Fatal("resolved 0 rows, want at least the exact-match link")
	}

	tests := []struct {
		targetURL  string
		wantStatus int32
		wantValid  bool
	}{
		{"https://example.com/gone", 404, true},
		{"https://example.com/moved/", 301, true},
		{"https://example.com/gone#section", 404, true},
		{"https://EXAMPLE.com/gone", 404, true},
		{"https://example.com/gone?x=1", 0, false},
		{"https://other.test/page", 0, false},
		{"https://example.com/never-crawled", 0, false},
	}

	for _, test := range tests {
		t.Run(test.targetURL, func(t *testing.T) {
			gotStatus, gotValid := targetStatusFor(t, ctx, pool, crawlID, test.targetURL)
			if gotValid != test.wantValid {
				t.Fatalf("target_status valid = %v, want %v", gotValid, test.wantValid)
			}
			if gotValid && gotStatus != test.wantStatus {
				t.Errorf("target_status = %d, want %d", gotStatus, test.wantStatus)
			}
		})
	}
}

// The resolve must not reach across crawls: one crawl's broken page cannot
// explain another crawl's link.
func TestResolveInternalLinkTargetStatusesIsCrawlScoped(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)
	otherCrawlID, cleanupOther := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanupOther)

	insertResolveTestPage(t, ctx, pool, otherCrawlID, "https://example.com/gone", 404)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/gone", true)

	if _, err := store.ResolveInternalLinkTargetStatuses(ctx, crawlID); err != nil {
		t.Fatalf("ResolveInternalLinkTargetStatuses: %v", err)
	}

	if _, valid := targetStatusFor(t, ctx, pool, crawlID, "https://example.com/gone"); valid {
		t.Error("target_status was filled from another crawl's page")
	}
}
