package issues

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func cancelTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	if _, currentFilePath, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
		_ = godotenv.Load(filepath.Join(repoRoot, ".env"))
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func mustParseUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse uuid: %v", err)
	}
	return id
}

func TestCancelledDeriveRetainsPreviousIssues(t *testing.T) {
	pool, ctx := cancelTestPool(t)
	store := NewStore(pool)

	var orgID, projectID, crawlID string
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('derive-cancel-org') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'derive-cancel', 'https://93.184.216.34') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1, 'running') RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	for _, pageURL := range []string{"https://93.184.216.34/a", "https://93.184.216.34/b"} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url) VALUES ($1, $2)`, crawlID, pageURL); err != nil {
			t.Fatalf("create page: %v", err)
		}
	}
	for _, pageURL := range []string{"https://93.184.216.34/a", "https://93.184.216.34/b"} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details)
			VALUES ($1, $2, 'seo', 'serp_metadata', 'missing_title', 'high', 'previous', 'previous')`, crawlID, pageURL); err != nil {
			t.Fatalf("create issue: %v", err)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	derived, pages, err := store.DeriveIssuesWithPagesAndSiteFacts(cancelled, mustParseUUID(t, crawlID), shared.SiteFacts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if derived != 0 || pages != nil {
		t.Fatalf("cancelled derive returned %d issues and %d pages, want none", derived, len(pages))
	}

	var issueCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM crawl_issues WHERE crawl_id = $1`, crawlID).Scan(&issueCount); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 2 {
		t.Fatalf("issue count after cancelled derive = %d, want previous 2 retained", issueCount)
	}
	var message string
	if err := pool.QueryRow(ctx, `SELECT message FROM crawl_issues WHERE crawl_id = $1 LIMIT 1`, crawlID).Scan(&message); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if message != "previous" {
		t.Fatalf("issue message = %q, previous rows must be untouched", message)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM crawls WHERE id = $1`, crawlID).Scan(&status); err != nil {
		t.Fatalf("read crawl: %v", err)
	}
	if status != "running" {
		t.Fatalf("crawl status = %q, cancelled derive must not write terminal state", status)
	}
}
