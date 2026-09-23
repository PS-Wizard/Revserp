package crawler

import (
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestResolveInternalLinkTargetStatusesMatchesNormalizedURLs(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)

	for _, url := range []string{
		"https://example.com/page",
		"https://example.com/page/",
		"https://EXAMPLE.com/page",
		"https://example.com/page#top",
	} {
		insertResolveTestPage(t, ctx, pool, crawlID, url, 404)
	}

	for _, targetURL := range []string{
		"https://EXAMPLE.com/page/#section",
		"https://example.com/PAGE",
		"https://example.com/page///",
		"https://EXAMPLE.com/page/#other",
	} {
		insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", targetURL, true)
	}
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/never-crawled", true)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://other.test/page", false)

	resolved, err := store.ResolveInternalLinkTargetStatuses(ctx, crawlID)
	if err != nil {
		t.Fatalf("ResolveInternalLinkTargetStatuses: %v", err)
	}
	if resolved != 4 {
		t.Fatalf("resolved %d links, want 4", resolved)
	}

	for _, targetURL := range []string{
		"https://EXAMPLE.com/page/#section",
		"https://example.com/PAGE",
		"https://example.com/page///",
		"https://EXAMPLE.com/page/#other",
	} {
		status, valid := targetStatusFor(t, ctx, pool, crawlID, targetURL)
		if !valid || status != 404 {
			t.Errorf("target_status for %q = %d, valid %v; want 404, valid true", targetURL, status, valid)
		}
	}
	for _, targetURL := range []string{"https://example.com/never-crawled", "https://other.test/page"} {
		if _, valid := targetStatusFor(t, ctx, pool, crawlID, targetURL); valid {
			t.Errorf("target_status for %q is set, want NULL", targetURL)
		}
	}
}

func TestResolveInternalLinkTargetStatusesLoopsInBatches(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)

	const links = 5
	for i := range links {
		pageURL := "https://example.com/page-" + string(rune('0'+i))
		insertResolveTestPage(t, ctx, pool, crawlID, pageURL, 200+i)
		insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", pageURL+"/", true)
	}

	resolved, err := store.resolveInternalLinkTargetStatusesInBatches(ctx, crawlID, 2)
	if err != nil {
		t.Fatalf("resolveInternalLinkTargetStatusesInBatches: %v", err)
	}
	if resolved != links {
		t.Fatalf("resolved %d links, want %d", resolved, links)
	}

	var resolvedLinks int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM crawl_links
		WHERE crawl_id = $1 AND target_status IS NOT NULL
	`, crawlID).Scan(&resolvedLinks); err != nil {
		t.Fatalf("count resolved links: %v", err)
	}
	if resolvedLinks != links {
		t.Fatalf("resolved link count = %d, want %d", resolvedLinks, links)
	}
}

// crawl_pages only enforces UNIQUE (crawl_id, url), so several crawled pages can
// share one normalized key and a plain join returns the same link once per
// matching page. A pass therefore affects fewer rows than it selected, and a
// caller that stops when a pass comes up short would leave links unresolved.
// This is the regression the DISTINCT ON and the affected-count loop guard.
func TestResolveInternalLinkTargetStatusesResolvesEveryLinkWithDuplicatePageKeys(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)

	// Two crawled pages, one normalized key, different statuses.
	insertResolveTestPage(t, ctx, pool, crawlID, "https://example.com/dup", 200)
	insertResolveTestPage(t, ctx, pool, crawlID, "https://example.com/dup/", 404)

	const links = 6
	for i := range links {
		sourceURL := "https://example.com/src-" + string(rune('0'+i))
		insertResolveTestLink(t, ctx, pool, crawlID, sourceURL, "https://example.com/dup", true)
	}

	// A batch smaller than the link count forces several passes over rows that
	// each fan out to two pages.
	resolved, err := store.resolveInternalLinkTargetStatusesInBatches(ctx, crawlID, 2)
	if err != nil {
		t.Fatalf("resolveInternalLinkTargetStatusesInBatches: %v", err)
	}
	if resolved != links {
		t.Fatalf("resolved %d links, want %d", resolved, links)
	}

	var unresolved int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM crawl_links
		WHERE crawl_id = $1 AND is_internal AND target_status IS NULL
	`, crawlID).Scan(&unresolved); err != nil {
		t.Fatalf("count unresolved links: %v", err)
	}
	if unresolved != 0 {
		t.Fatalf("%d internal links left unresolved, want 0", unresolved)
	}

	// Ordering by status_code makes the pick deterministic when pages disagree.
	status, valid := targetStatusFor(t, ctx, pool, crawlID, "https://example.com/dup")
	if !valid || status != 200 {
		t.Fatalf("target_status = %d, valid %v; want the lowest status 200", status, valid)
	}
}

func TestResolveInternalLinkTargetStatusesLeavesNullStatusPagesUnresolved(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)

	// A crawled page with no status code cannot answer "does this target exist".
	if _, err := pool.Exec(ctx,
		`INSERT INTO crawl_pages (crawl_id, url) VALUES ($1, $2)`,
		crawlID, "https://example.com/no-status"); err != nil {
		t.Fatalf("insert page without status: %v", err)
	}
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/no-status", true)

	resolved, err := store.ResolveInternalLinkTargetStatuses(ctx, crawlID)
	if err != nil {
		t.Fatalf("ResolveInternalLinkTargetStatuses: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("resolved %d links, want 0", resolved)
	}
	if _, valid := targetStatusFor(t, ctx, pool, crawlID, "https://example.com/no-status"); valid {
		t.Fatal("target_status is set for a page with no status code, want NULL")
	}
}

// The resolve only fills NULLs, so a status the create-link API supplied is kept.
func TestResolveInternalLinkTargetStatusesKeepsExistingStatus(t *testing.T) {
	store, pool, crawlID, ctx := newResolveTestStore(t)

	insertResolveTestPage(t, ctx, pool, crawlID, "https://example.com/gone", 404)
	insertResolveTestLink(t, ctx, pool, crawlID, "https://example.com/", "https://example.com/gone", true)
	if _, err := pool.Exec(ctx, `
		UPDATE crawl_links SET target_status = 999 WHERE crawl_id = $1
	`, crawlID); err != nil {
		t.Fatalf("seed existing target_status: %v", err)
	}

	if _, err := store.ResolveInternalLinkTargetStatuses(ctx, crawlID); err != nil {
		t.Fatalf("ResolveInternalLinkTargetStatuses: %v", err)
	}

	status, valid := targetStatusFor(t, ctx, pool, crawlID, "https://example.com/gone")
	if !valid || status != 999 {
		t.Fatalf("target_status = %d, valid %v; want the existing 999 kept", status, valid)
	}
}

// A reused link is reused because its SOURCE page answered 304. Its target's
// status is a fact about this crawl, not the last one. The baseline copy must
// therefore leave target_status NULL: the resolver only fills NULLs, so a
// carried-over status would never be corrected and broken-target issues would
// silently vanish whenever a target changed while its source did not.
func TestResolveInternalLinkTargetStatusesRefreshesCopiedBaselineStatus(t *testing.T) {
	store, pool, baselineCrawlID, ctx := newResolveTestStore(t)

	currentCrawlID, cleanupCurrent := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanupCurrent)

	// Baseline: the target answered 200, and the link recorded that.
	insertResolveTestPage(t, ctx, pool, baselineCrawlID, "https://example.com/target", 200)
	insertResolveTestLink(t, ctx, pool, baselineCrawlID, "https://example.com/source", "https://example.com/target", true)
	if _, err := pool.Exec(ctx,
		`UPDATE crawl_links SET target_status = 200 WHERE crawl_id = $1`,
		baselineCrawlID); err != nil {
		t.Fatalf("seed baseline target_status: %v", err)
	}

	// This crawl: the source was reused, but the target now answers 404.
	insertResolveTestPage(t, ctx, pool, currentCrawlID, "https://example.com/target", 404)
	if _, err := store.queries.CopyCrawlLinksFromBaseline(ctx, sqlc.CopyCrawlLinksFromBaselineParams{
		CrawlID:         currentCrawlID,
		BaselineCrawlID: baselineCrawlID,
		SourceUrl:       "https://example.com/source",
	}); err != nil {
		t.Fatalf("copy crawl links from baseline: %v", err)
	}

	if _, err := store.ResolveInternalLinkTargetStatuses(ctx, currentCrawlID); err != nil {
		t.Fatalf("ResolveInternalLinkTargetStatuses: %v", err)
	}

	status, valid := targetStatusFor(t, ctx, pool, currentCrawlID, "https://example.com/target")
	if !valid || status != 404 {
		t.Fatalf("target_status = %d, valid %v; want the current crawl's 404", status, valid)
	}
}
