package issues

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const issuesScratchDatabaseEnv = "ISSUES_SCRATCH_TEST_DATABASE_URL"

func issuesScratchPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	raw := os.Getenv(issuesScratchDatabaseEnv)
	if raw == "" {
		t.Skip(issuesScratchDatabaseEnv + " is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", issuesScratchDatabaseEnv, err)
	}
	if name := strings.Trim(parsed.Path, "/"); !strings.HasPrefix(name, "revserp_layer_c_") {
		t.Fatalf("refusing %s database %q: want a revserp_layer_c_* scratch database", issuesScratchDatabaseEnv, name)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("refusing %s host %q: want 127.0.0.1 or localhost", issuesScratchDatabaseEnv, host)
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, raw, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("%s is not available: %v", issuesScratchDatabaseEnv, err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func TestScratchDeriveLinkTargetsWithoutBackfill(t *testing.T) {
	pool, ctx := issuesScratchPool(t)
	store := NewStore(pool)

	var orgID, projectID, crawlID string
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('link-status-scratch-org') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'link-status-scratch', 'https://scratch.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1, 'completed') RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	pages := []struct {
		url    string
		status int
	}{
		{"https://scratch.example/", 200},
		{"https://scratch.example/gone", 404},
		{"https://scratch.example/moved", 301},
	}
	for _, page := range pages {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, $2, $3)`, crawlID, page.url, page.status); err != nil {
			t.Fatalf("create page %s: %v", page.url, err)
		}
	}
	for _, target := range []string{"https://scratch.example/gone", "https://scratch.example/moved", "https://scratch.example/missing"} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal) VALUES ($1, $2, $3, TRUE)`, crawlID, "https://scratch.example/", target); err != nil {
			t.Fatalf("create link to %s: %v", target, err)
		}
	}

	parsedCrawlID := mustParseUUID(t, crawlID)
	if _, _, err := store.DeriveIssuesWithPagesAndSiteFacts(ctx, parsedCrawlID, shared.SiteFacts{}); err != nil {
		t.Fatalf("derive issues: %v", err)
	}

	var nullTargets int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM crawl_links WHERE crawl_id = $1 AND target_status IS NULL`, crawlID).Scan(&nullTargets); err != nil {
		t.Fatalf("count null target statuses: %v", err)
	}
	if nullTargets != 3 {
		t.Fatalf("target_status null count = %d, want 3 (no backfill ran)", nullTargets)
	}

	detailsByType := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT issue_type, details FROM crawl_issues WHERE crawl_id = $1 AND issue_type IN ('internal_links_to_broken_pages', 'internal_links_to_redirects')`, crawlID)
	if err != nil {
		t.Fatalf("list link issues: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var issueType, details string
		if err := rows.Scan(&issueType, &details); err != nil {
			t.Fatalf("scan link issue: %v", err)
		}
		detailsByType[issueType] = details
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read link issues: %v", err)
	}
	broken, ok := detailsByType["internal_links_to_broken_pages"]
	if !ok || !strings.Contains(broken, "https://scratch.example/gone") {
		t.Fatalf("broken-pages issue details = %q, want it naming the 404 target", broken)
	}
	redirecting, ok := detailsByType["internal_links_to_redirects"]
	if !ok || !strings.Contains(redirecting, "https://scratch.example/moved") {
		t.Fatalf("redirects issue details = %q, want it naming the 301 target", redirecting)
	}
	for issueType, details := range detailsByType {
		if strings.Contains(details, "https://scratch.example/missing") {
			t.Fatalf("%s names unfetched target /missing, which must stay unclassified", issueType)
		}
	}
}
