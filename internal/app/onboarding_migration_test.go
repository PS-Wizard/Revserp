package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
)

const onboardingMigrationScratchDBName = "revserp_onboarding_migration_test"

func onboardingMigrationScratchDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	var migrationsDir string
	if _, currentFilePath, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
		_ = godotenv.Load(filepath.Join(repoRoot, ".env"))
		migrationsDir = filepath.Join(repoRoot, "migrations")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" || migrationsDir == "" {
		t.Skip("DATABASE_URL is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Skipf("DATABASE_URL is not parseable: %v", err)
	}

	adminURL := *parsed
	adminURL.Path = "/postgres"
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("database is not available: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+onboardingMigrationScratchDBName+`" WITH (FORCE)`); err != nil {
		t.Fatalf("drop stale scratch database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+onboardingMigrationScratchDBName+`"`); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}

	scratchURL := *parsed
	scratchURL.Path = "/" + onboardingMigrationScratchDBName
	db, err := sql.Open("pgx", scratchURL.String())
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("ping scratch database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		admin, err := sql.Open("pgx", adminURL.String())
		if err != nil {
			return
		}
		defer admin.Close()
		_, _ = admin.ExecContext(cleanupCtx, `DROP DATABASE IF EXISTS "`+onboardingMigrationScratchDBName+`" WITH (FORCE)`)
	})
	return db, migrationsDir
}

func onboardingScratchUser(t *testing.T, ctx context.Context, db *sql.DB, subject string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`,
		subject, subject+"@example.com").Scan(&id); err != nil {
		t.Fatalf("create scratch user: %v", err)
	}
	return id
}

func onboardingScratchUserStatus(t *testing.T, ctx context.Context, db *sql.DB, id string) (string, string) {
	t.Helper()
	var status, checkpoint string
	if err := db.QueryRowContext(ctx,
		`SELECT onboarding_state->>'status', onboarding_state->>'checkpoint' FROM users WHERE id = $1`,
		id).Scan(&status, &checkpoint); err != nil {
		t.Fatalf("read scratch onboarding state: %v", err)
	}
	return status, checkpoint
}

func TestOnboardingMigrationUpDownIsolated(t *testing.T) {
	db, migrationsDir := onboardingMigrationScratchDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, db, migrationsDir, 109); err != nil {
		t.Fatalf("migrate scratch database to 109: %v", err)
	}
	var columnCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'onboarding_state'`).Scan(&columnCount); err != nil {
		t.Fatalf("check pre-migration column: %v", err)
	}
	if columnCount != 0 {
		t.Fatal("onboarding_state column exists before migration 110")
	}

	existingID := onboardingScratchUser(t, ctx, db, fmt.Sprintf("onboarding-existing-%d", time.Now().UnixNano()))

	if err := goose.UpToContext(ctx, db, migrationsDir, 110); err != nil {
		t.Fatalf("migrate scratch database to 110: %v", err)
	}
	if status, checkpoint := onboardingScratchUserStatus(t, ctx, db, existingID); status != "skipped" || checkpoint != "new_project" {
		t.Fatalf("existing user state = %s/%s, want skipped/new_project", status, checkpoint)
	}
	freshID := onboardingScratchUser(t, ctx, db, fmt.Sprintf("onboarding-fresh-%d", time.Now().UnixNano()))
	if status, checkpoint := onboardingScratchUserStatus(t, ctx, db, freshID); status != "not_started" || checkpoint != "new_project" {
		t.Fatalf("fresh user state = %s/%s, want not_started/new_project", status, checkpoint)
	}

	if err := goose.DownToContext(ctx, db, migrationsDir, 109); err != nil {
		t.Fatalf("roll back scratch database to 109: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'onboarding_state'`).Scan(&columnCount); err != nil {
		t.Fatalf("check rolled-back column: %v", err)
	}
	if columnCount != 0 {
		t.Fatal("onboarding_state column survives rollback of migration 110")
	}

	if err := goose.UpToContext(ctx, db, migrationsDir, 110); err != nil {
		t.Fatalf("re-apply migration 110: %v", err)
	}
	for _, id := range []string{existingID, freshID} {
		if status, checkpoint := onboardingScratchUserStatus(t, ctx, db, id); status != "skipped" || checkpoint != "new_project" {
			t.Fatalf("re-applied state for %s = %s/%s, want skipped/new_project", id, status, checkpoint)
		}
	}
}
