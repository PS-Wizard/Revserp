package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The worker must see an admin saved override written after process startup:
// env 21 with no row means 21, an upsert of 42 means the next resolve on the
// same instance returns 42, and a reset DELETE means 21 again.
//
// Everything runs inside one rollback-only transaction: the global singleton
// row is only ever touched inside tx, so the live override is never altered.
// Deliberately not parallel: the open transaction holds the singleton row.
func TestResolveCrawlPageWorkerCountSeesAdminOverride(t *testing.T) {
	if _, currentFilePath, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
		_ = godotenv.Load(filepath.Join(repoRoot, ".env"))
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("DB")
	}
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})

	if _, err := tx.Exec(ctx, `DELETE FROM crawl_page_worker_config`); err != nil {
		t.Fatalf("clear crawl page worker config: %v", err)
	}

	w := &Worker{
		pool:    pool,
		queries: sqlc.New(pool).WithTx(tx),
		cfg:     Config{CrawlPageWorkerCount: 21},
	}

	if got, err := w.resolveCrawlPageWorkerCount(ctx); err != nil || got != 21 {
		t.Fatalf("resolve with no row = %d, %v; want 21, nil", got, err)
	}

	if _, err := w.queries.UpsertCrawlPageWorkerConfig(ctx, sqlc.UpsertCrawlPageWorkerConfigParams{
		WorkerCount:     42,
		UpdatedByUserID: pgtype.UUID{},
	}); err != nil {
		t.Fatalf("upsert override 42: %v", err)
	}
	if got, err := w.resolveCrawlPageWorkerCount(ctx); err != nil || got != 42 {
		t.Fatalf("resolve after upsert 42 = %d, %v; want 42, nil", got, err)
	}
	if w.cfg.CrawlPageWorkerCount != 21 {
		t.Fatalf("resolve mutated w.cfg to %d; want boot-time 21 intact", w.cfg.CrawlPageWorkerCount)
	}

	if err := w.queries.ResetCrawlPageWorkerConfig(ctx); err != nil {
		t.Fatalf("reset override: %v", err)
	}
	if got, err := w.resolveCrawlPageWorkerCount(ctx); err != nil || got != 21 {
		t.Fatalf("resolve after reset = %d, %v; want 21, nil", got, err)
	}
}
