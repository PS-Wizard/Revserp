package crawler

import (
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestBaselineCopiedLinksHaveNullTargetStatus(t *testing.T) {
	pool, ctx := crawlerScratchPool(t)
	store := NewStore(pool)
	baselineCrawlID, cleanupBaseline := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanupBaseline)
	currentCrawlID, cleanupCurrent := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanupCurrent)

	if _, err := pool.Exec(ctx,
		`INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, $2, $3)`,
		baselineCrawlID, "https://example.com/target", 200); err != nil {
		t.Fatalf("seed baseline page: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal, target_status) VALUES ($1, $2, $3, TRUE, 200)`,
		baselineCrawlID, "https://example.com/source", "https://example.com/target"); err != nil {
		t.Fatalf("seed baseline link: %v", err)
	}
	if _, err := store.queries.CopyCrawlLinksFromBaseline(ctx, sqlc.CopyCrawlLinksFromBaselineParams{
		CrawlID:         currentCrawlID,
		BaselineCrawlID: baselineCrawlID,
		SourceUrl:       "https://example.com/source",
	}); err != nil {
		t.Fatalf("copy crawl links from baseline: %v", err)
	}

	var copiedNull bool
	if err := pool.QueryRow(ctx, `SELECT target_status IS NULL FROM crawl_links
		WHERE crawl_id = $1 AND target_url = $2`,
		currentCrawlID, "https://example.com/target").Scan(&copiedNull); err != nil {
		t.Fatalf("read copied link status: %v", err)
	}
	if !copiedNull {
		t.Fatal("copied baseline link keeps a target status, want NULL for the current crawl to resolve")
	}
}
