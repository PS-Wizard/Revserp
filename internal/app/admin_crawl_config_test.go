package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNewCrawlConfigResponse(t *testing.T) {
	got := newCrawlConfigResponse(4, nil)
	if got.WorkerCount != 4 || got.EnvWorkerCount != 4 || got.OverrideWorkerCount != nil || got.Source != "env" {
		t.Errorf("no override = %+v; want effective 4 from env", got)
	}

	override := 12
	got = newCrawlConfigResponse(4, &override)
	if got.WorkerCount != 12 || got.EnvWorkerCount != 4 || got.OverrideWorkerCount == nil || *got.OverrideWorkerCount != 12 || got.Source != "admin" {
		t.Errorf("override 12 = %+v; want effective 12 from admin", got)
	}
}

// newCrawlConfigTestApp wires the handlers against a real Postgres, like the
// other *_db_test.go suites. It reports the boot-time env count as 7 so the
// tests can tell env and override values apart.
//
// Everything runs inside one rollback-only transaction: the live singleton
// row (including any persisted admin override) and the users table are never
// modified by this test. Deliberately not parallel: the open transaction
// holds the singleton row lock.
func newCrawlConfigTestApp(t *testing.T) (*App, pgtype.UUID, pgx.Tx) {
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

	var userID pgtype.UUID
	subject := fmt.Sprintf("crawl-config-test-%d", time.Now().UnixNano())
	err = tx.QueryRow(ctx,
		`INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, 'crawl-config-test@example.com') RETURNING id`, subject).Scan(&userID)
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}

	app := &App{
		Config:  config.Config{CrawlPageWorkerCount: 7},
		DB:      pool,
		Queries: sqlc.New(pool).WithTx(tx),
	}
	return app, userID, tx
}

func decodeCrawlConfigResponse(t *testing.T, rec *httptest.ResponseRecorder) crawlConfigResponse {
	t.Helper()
	var resp crawlConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode crawl config response: %v", err)
	}
	return resp
}

func TestAdminCrawlConfigHandlers(t *testing.T) {
	app, userID, tx := newCrawlConfigTestApp(t)

	// GET with no override reports env and writes nothing.
	rec := httptest.NewRecorder()
	app.handleAdminGetCrawlConfig(rec, httptest.NewRequest(http.MethodGet, "/admin/crawl-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	if got := decodeCrawlConfigResponse(t, rec); got.WorkerCount != 7 || got.EnvWorkerCount != 7 || got.OverrideWorkerCount != nil || got.Source != "env" {
		t.Fatalf("GET no override = %+v; want effective 7 from env", got)
	}
	if _, err := app.Queries.GetCrawlPageWorkerConfig(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GET created a row: %v", err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/admin/crawl-config", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), cachedUserContextKey{}, sqlc.User{ID: userID}))
		rec := httptest.NewRecorder()
		app.handleAdminPutCrawlConfig(rec, req)
		return rec
	}

	// PUT valid.
	if rec := put(`{"worker_count": 12}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT valid status = %d, want 200", rec.Code)
	} else if got := decodeCrawlConfigResponse(t, rec); got.WorkerCount != 12 || got.EnvWorkerCount != 7 || got.OverrideWorkerCount == nil || *got.OverrideWorkerCount != 12 || got.Source != "admin" {
		t.Fatalf("PUT valid = %+v; want effective 12 from admin", got)
	}

	// PUT invalid: every case must be 400 and leave the saved 12 untouched.
	for _, body := range []string{
		`{}`,
		``,
		`{"worker_count": 0}`,
		`{"worker_count": -3}`,
		`{"worker_count": 101}`,
		`{"worker_count": 4.5}`,
		`{"worker_count": 4.0}`,
		`{"worker_count": "9"}`,
		`{"worker_count": null}`,
		`not json`,
	} {
		if rec := put(body); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %q status = %d, want 400", body, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	app.handleAdminGetCrawlConfig(rec, httptest.NewRequest(http.MethodGet, "/admin/crawl-config", nil))
	if got := decodeCrawlConfigResponse(t, rec); got.WorkerCount != 12 || got.OverrideWorkerCount == nil || *got.OverrideWorkerCount != 12 || got.Source != "admin" {
		t.Fatalf("GET after invalid PUTs = %+v; want saved 12 intact", got)
	}

	// Boundaries 1 and 100 are accepted.
	if rec := put(`{"worker_count": 1}`); rec.Code != http.StatusOK {
		t.Errorf("PUT 1 status = %d, want 200", rec.Code)
	}
	if rec := put(`{"worker_count": 100}`); rec.Code != http.StatusOK {
		t.Errorf("PUT 100 status = %d, want 200", rec.Code)
	} else if got := decodeCrawlConfigResponse(t, rec); got.WorkerCount != 100 {
		t.Errorf("PUT 100 = %+v; want effective 100", got)
	}

	// Reset with malformed JSON must 400 and preserve the saved override.
	badResetReq := httptest.NewRequest(http.MethodPost, "/admin/crawl-config/reset", strings.NewReader(`not json`))
	badResetRec := httptest.NewRecorder()
	app.handleAdminResetCrawlConfig(badResetRec, badResetReq)
	if badResetRec.Code != http.StatusBadRequest {
		t.Fatalf("reset malformed status = %d, want 400", badResetRec.Code)
	}
	rec = httptest.NewRecorder()
	app.handleAdminGetCrawlConfig(rec, httptest.NewRequest(http.MethodGet, "/admin/crawl-config", nil))
	if got := decodeCrawlConfigResponse(t, rec); got.WorkerCount != 100 || got.OverrideWorkerCount == nil || *got.OverrideWorkerCount != 100 || got.Source != "admin" {
		t.Fatalf("GET after malformed reset = %+v; want saved 100 intact", got)
	}

	// The DB CHECK constraint backs the handler validation. The intentional
	// failure runs under a savepoint so it cannot abort the test transaction.
	if _, err := tx.Exec(context.Background(), `SAVEPOINT crawl_config_check`); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	if _, err := app.Queries.UpsertCrawlPageWorkerConfig(context.Background(), sqlc.UpsertCrawlPageWorkerConfigParams{WorkerCount: 0, UpdatedByUserID: userID}); err == nil {
		t.Error("upsert worker_count 0 succeeded; want a check-constraint error")
	}
	if _, err := tx.Exec(context.Background(), `ROLLBACK TO SAVEPOINT crawl_config_check`); err != nil {
		t.Fatalf("rollback to savepoint: %v", err)
	}

	// Reset deletes the row: env wins again.
	resetReq := httptest.NewRequest(http.MethodPost, "/admin/crawl-config/reset", strings.NewReader(`{}`))
	resetRec := httptest.NewRecorder()
	app.handleAdminResetCrawlConfig(resetRec, resetReq)
	if resetRec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", resetRec.Code)
	}
	if got := decodeCrawlConfigResponse(t, resetRec); got.WorkerCount != 7 || got.EnvWorkerCount != 7 || got.OverrideWorkerCount != nil || got.Source != "env" {
		t.Fatalf("reset = %+v; want effective 7 from env", got)
	}
	if _, err := app.Queries.GetCrawlPageWorkerConfig(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reset left a row: %v", err)
	}

}
