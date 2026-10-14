package sqlc

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListCrawlLinksForCrawlByUserUsesBoundedDigestLookup(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CRAWL_LINK_STATUS_SCRATCH_DATABASE_URL"))
	if dsn == "" {
		t.Skip("CRAWL_LINK_STATUS_SCRATCH_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" || parsed.Hostname() != "127.0.0.1" || parsed.Port() != "18547" || strings.TrimPrefix(parsed.Path, "/") != "revserp_layer_c_link_status_20261014" {
		t.Skip("scratch DSN failed safety verification")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("scratch database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	var orgID, projectID, crawlUUID string
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('link-plan-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'link-plan-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'running') RETURNING id`, projectID).Scan(&crawlUUID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) SELECT $1, 'https://example.com/bulk' || g, 200 + (g % 3) FROM generate_series(1, 3000) g`, crawlUUID); err != nil {
		t.Fatalf("bulk pages: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal) SELECT $1, 'https://example.com/src' || g, 'https://example.com/bulk' || g, TRUE FROM generate_series(1, 3000) g`, crawlUUID); err != nil {
		t.Fatalf("bulk links: %v", err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE crawl_pages`); err != nil {
		t.Fatalf("analyze pages: %v", err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE crawl_links`); err != nil {
		t.Fatalf("analyze links: %v", err)
	}

	var plan strings.Builder
	rows, err := tx.Query(ctx, `EXPLAIN `+listCrawlLinksForCrawlByUser, crawlUUID, "00000000-0000-0000-0000-000000000000", int32(5), int32(0))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if !strings.Contains(plan.String(), "idx_crawl_pages_crawl_id_url_key_digest") {
		t.Fatalf("plan misses the digest lookup:\n%s", plan.String())
	}
}
