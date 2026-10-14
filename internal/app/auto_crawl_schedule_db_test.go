package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// newAutoCrawlScheduleTestScope seeds one org, user, project, and owner
// membership against a real Postgres, like the other *_db_test.go suites.
// Everything is deleted on cleanup. Skips when DATABASE_URL is absent, and
// requires migration 000113 applied for 60-day rows (the old 1..30 check
// rejects them).
func newAutoCrawlScheduleTestScope(t *testing.T) (*App, context.Context, pgtype.UUID, pgtype.UUID) {
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

	var orgID, userID, projectID pgtype.UUID
	subject := fmt.Sprintf("auto-crawl-schedule-test-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('auto-crawl-schedule-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create test organization: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, 'auto-crawl-schedule-test@example.com') RETURNING id`, subject).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatalf("create test membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'auto-crawl-schedule-test', 'https://auto-crawl-schedule-test.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create test project: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	app := &App{DB: pool, Queries: sqlc.New(pool)}
	return app, ctx, projectID, userID
}

func autoCrawlTestFrequency(days int32) *int32 { return &days }

// requireAutoCrawlSixtyDaySchema skips the round trip when the database
// predates migration 000113: the old 1..30 check would reject the 60-day
// row, and migrations are never applied by tests.
func requireAutoCrawlSixtyDaySchema(t *testing.T, ctx context.Context, app *App) {
	t.Helper()
	var clause string
	if err := app.DB.QueryRow(ctx, `SELECT check_clause FROM information_schema.check_constraints WHERE constraint_name = 'project_auto_crawl_settings_frequency_days_check'`).Scan(&clause); err != nil {
		t.Skipf("auto-crawl frequency check constraint not found: %v", err)
	}
	if !strings.Contains(clause, "60") {
		t.Skipf("auto-crawl schema predates 60-day support (%s)", clause)
	}
}

// A 60-day schedule stores and re-saves byte-identical: re-saving unchanged
// settings or disabling must keep frequency_days and next_run_at, and 61 is
// rejected before touching the database.
func TestAutoCrawlSixtyDayScheduleRoundTrip(t *testing.T) {
	app, ctx, projectID, userID := newAutoCrawlScheduleTestScope(t)
	requireAutoCrawlSixtyDaySchema(t, ctx, app)
	queries := app.Queries

	enabled, err := app.applyAutoCrawlSettings(ctx, queries, projectID, userID, autoCrawlSettingsInput{
		Enabled:       true,
		FrequencyDays: autoCrawlTestFrequency(60),
	})
	if err != nil {
		t.Fatalf("enable 60-day schedule: %v", err)
	}
	if enabled.FrequencyDays != 60 {
		t.Fatalf("stored frequency_days = %d, want 60", enabled.FrequencyDays)
	}
	if !enabled.NextRunAt.Valid {
		t.Fatal("enabled schedule must precompute next_run_at")
	}

	resaved, err := app.applyAutoCrawlSettings(ctx, queries, projectID, userID, autoCrawlSettingsInput{
		Enabled:       true,
		FrequencyDays: autoCrawlTestFrequency(60),
	})
	if err != nil {
		t.Fatalf("re-save unchanged schedule: %v", err)
	}
	if resaved.FrequencyDays != 60 {
		t.Fatalf("re-saved frequency_days = %d, want preserved 60", resaved.FrequencyDays)
	}
	if !resaved.NextRunAt.Valid || !resaved.NextRunAt.Time.Equal(enabled.NextRunAt.Time) {
		t.Fatalf("re-save moved next_run_at from %s to %s, want it kept", enabled.NextRunAt.Time, resaved.NextRunAt.Time)
	}

	disabled, err := app.applyAutoCrawlSettings(ctx, queries, projectID, userID, autoCrawlSettingsInput{
		Enabled:       false,
		FrequencyDays: autoCrawlTestFrequency(60),
	})
	if err != nil {
		t.Fatalf("disable schedule: %v", err)
	}
	if disabled.Enabled || disabled.FrequencyDays != 60 {
		t.Fatalf("disable must keep the stored schedule, got enabled=%v frequency=%d", disabled.Enabled, disabled.FrequencyDays)
	}
	if !disabled.NextRunAt.Valid || !disabled.NextRunAt.Time.Equal(enabled.NextRunAt.Time) {
		t.Fatal("disable must keep next_run_at")
	}

	if _, err := app.applyAutoCrawlSettings(ctx, queries, projectID, userID, autoCrawlSettingsInput{
		Enabled:       true,
		FrequencyDays: autoCrawlTestFrequency(61),
	}); err == nil {
		t.Fatal("61-day schedule succeeded, want rejection")
	} else if _, ok := err.(*autoCrawlBadRequestError); !ok {
		t.Fatalf("61-day schedule err = %T, want HTTP 400", err)
	}

	stored, err := queries.GetProjectAutoCrawlSettings(ctx, projectID)
	if err != nil {
		t.Fatalf("read back schedule: %v", err)
	}
	if stored.FrequencyDays != 60 {
		t.Fatalf("stored frequency_days = %d, want rejected write to leave 60", stored.FrequencyDays)
	}
}
