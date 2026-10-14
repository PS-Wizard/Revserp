package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const aiAuditMarkerScratchDBName = "revserp_ai_audit_marker_migration_test"

func aiAuditMarkerScratchDB(t *testing.T) (*sql.DB, string) {
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
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+aiAuditMarkerScratchDBName+`" WITH (FORCE)`); err != nil {
		t.Fatalf("drop stale scratch database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+aiAuditMarkerScratchDBName+`"`); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}

	scratchURL := *parsed
	scratchURL.Path = "/" + aiAuditMarkerScratchDBName
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
		_, _ = admin.ExecContext(cleanupCtx, `DROP DATABASE IF EXISTS "`+aiAuditMarkerScratchDBName+`" WITH (FORCE)`)
	})
	return db, migrationsDir
}

func aiAuditMarkerColumnCount(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'ai_audits' AND column_name = 'project_setup_id'`).Scan(&count); err != nil {
		t.Fatalf("check marker column: %v", err)
	}
	return count
}

func aiAuditMarkerTriggerDef(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var definition string
	if err := db.QueryRowContext(ctx,
		`SELECT pg_get_functiondef('organization_event_from_ai_audit()'::regprocedure)`).Scan(&definition); err != nil {
		t.Fatalf("read trigger definition: %v", err)
	}
	return definition
}

func TestAIAuditMarkerMigrationUpDownIsolated(t *testing.T) {
	db, migrationsDir := aiAuditMarkerScratchDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, db, migrationsDir, 110); err != nil {
		t.Fatalf("migrate scratch database to 110: %v", err)
	}
	if got := aiAuditMarkerColumnCount(t, ctx, db); got != 0 {
		t.Fatal("project_setup_id column exists before migration 111")
	}

	var orgID, projectID, crawlID, auditID string
	if err := db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('marker-mig') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'marker-mig', 'https://93.184.216.34') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1, 'completed') RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status) VALUES ($1, $2, 'completed') RETURNING id`,
		projectID, crawlID).Scan(&auditID); err != nil {
		t.Fatalf("create legacy audit: %v", err)
	}

	if err := goose.UpToContext(ctx, db, migrationsDir, 111); err != nil {
		t.Fatalf("migrate scratch database to 111: %v", err)
	}
	if got := aiAuditMarkerColumnCount(t, ctx, db); got != 1 {
		t.Fatal("project_setup_id column missing after migration 111")
	}
	var isNull bool
	if err := db.QueryRowContext(ctx, `SELECT project_setup_id IS NULL FROM ai_audits WHERE id = $1`, auditID).Scan(&isNull); err != nil {
		t.Fatalf("read legacy marker: %v", err)
	}
	if !isNull {
		t.Fatal("legacy audit gained a marker on migrate; no backfill guessing allowed")
	}
	if definition := aiAuditMarkerTriggerDef(t, ctx, db); !strings.Contains(definition, "project_setup_id") {
		t.Fatal("upgraded trigger does not emit project_setup_id")
	}

	var setupID string
	if err := db.QueryRowContext(ctx, `INSERT INTO project_setup (organization_id, project_id, crawl_id, status)
		VALUES ($1, $2, $3, 'visibility') RETURNING id`, orgID, projectID, crawlID).Scan(&setupID); err != nil {
		t.Fatalf("create setup: %v", err)
	}
	var markedID string
	if err := db.QueryRowContext(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status, project_setup_id)
		VALUES ($1, $2, 'queued', $3) RETURNING id`, projectID, crawlID, setupID).Scan(&markedID); err != nil {
		t.Fatalf("create marked audit: %v", err)
	}
	var storedMarker string
	if err := db.QueryRowContext(ctx, `SELECT project_setup_id::text FROM ai_audits WHERE id = $1`, markedID).Scan(&storedMarker); err != nil {
		t.Fatalf("read marked audit: %v", err)
	}
	if storedMarker != setupID {
		t.Fatalf("stored marker = %s, want %s", storedMarker, setupID)
	}
	var payloadMarker *string
	if err := db.QueryRowContext(ctx, `SELECT payload->>'project_setup_id' FROM organization_events
		WHERE resource_id = $1 AND event_type = 'ai_audit.queued' ORDER BY id DESC LIMIT 1`, markedID).Scan(&payloadMarker); err != nil {
		t.Fatalf("read queued payload: %v", err)
	}
	if payloadMarker == nil || *payloadMarker != setupID {
		t.Fatalf("queued payload marker = %v, want %s", payloadMarker, setupID)
	}

	if _, err := db.ExecContext(ctx, `UPDATE ai_audits SET status = 'completed' WHERE id = $1`, markedID); err != nil {
		t.Fatalf("complete marked audit: %v", err)
	}

	if err := goose.DownToContext(ctx, db, migrationsDir, 110); err != nil {
		t.Fatalf("roll back scratch database to 110: %v", err)
	}
	if got := aiAuditMarkerColumnCount(t, ctx, db); got != 0 {
		t.Fatal("project_setup_id column survives rollback of migration 111")
	}
	if definition := aiAuditMarkerTriggerDef(t, ctx, db); strings.Contains(definition, "project_setup_id") {
		t.Fatal("rolled-back trigger still references the dropped column")
	}
	var postRollbackID string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`INSERT INTO ai_audits (project_id, crawl_id, status) VALUES ($1, $2, 'queued') RETURNING id`),
		projectID, crawlID).Scan(&postRollbackID); err != nil {
		t.Fatalf("create audit after rollback: %v", err)
	}
	var hasKey bool
	if err := db.QueryRowContext(ctx, `SELECT (payload ? 'project_setup_id') FROM organization_events
		WHERE resource_id = $1 AND event_type = 'ai_audit.queued' ORDER BY id DESC LIMIT 1`, postRollbackID).Scan(&hasKey); err != nil {
		t.Fatalf("read post-rollback payload: %v", err)
	}
	if hasKey {
		t.Fatal("post-rollback event still carries project_setup_id")
	}

	if err := goose.UpToContext(ctx, db, migrationsDir, 111); err != nil {
		t.Fatalf("re-apply migration 111: %v", err)
	}
	if got := aiAuditMarkerColumnCount(t, ctx, db); got != 1 {
		t.Fatal("project_setup_id column missing after re-applying migration 111")
	}
}
