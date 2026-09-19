package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration paths are relative to this package directory (internal/db), which
// is the CWD for `go test ./internal/db`.
const (
	migration000072Path = "../../migrations/000072_project_setup.sql"
	migration000073Path = "../../migrations/000073_project_setup_schema_repair.sql"
)

func readMigration(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func migrationUpSection(t *testing.T, sql string) string {
	t.Helper()
	up := strings.Index(sql, "-- +goose Up")
	down := strings.Index(sql, "-- +goose Down")
	if up < 0 || down < 0 || down < up {
		t.Fatalf("migration missing ordered goose Up/Down markers")
	}
	return sql[up:down]
}

func migrationDownSection(t *testing.T, sql string) string {
	t.Helper()
	down := strings.Index(sql, "-- +goose Down")
	if down < 0 {
		t.Fatalf("migration missing goose Down marker")
	}
	return sql[down:]
}

func assertContainsAll(t *testing.T, body string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestMigration000073RepairsOldDraftDefects asserts the repair closes every
// difference between the old draft 000072 and the finalized 000072.
func TestMigration000073RepairsOldDraftDefects(t *testing.T) {
	sql := readMigration(t, migration000073Path)

	if !strings.Contains(sql, "-- +goose Up") || !strings.Contains(sql, "-- +goose Down") {
		t.Fatal("repair migration must declare goose Up and Down")
	}
	up := migrationUpSection(t, sql)

	assertContainsAll(t, up, []string{
		// Missing column.
		"ADD COLUMN IF NOT EXISTS failed_step TEXT",
		// Final failed-step allowlist.
		"project_setup_failed_step_check",
		"'crawling'",
		"'profile_generation'",
		"'prompt_generation'",
		"'visibility'",
		// Final status allowlist including the draft-missing 'ready'.
		"project_setup_status_check",
		"'ready'",
		"'completed'",
		"'failed'",
		// Nullable requester with SET NULL instead of NOT NULL CASCADE.
		"ALTER COLUMN requested_by_user_id DROP NOT NULL",
		"ON DELETE SET NULL",
		"project_setup_requested_by_user_id_fkey",
		// Final trigger function and its event names/payload.
		"CREATE OR REPLACE FUNCTION organization_event_from_project_setup()",
		"'project_setup.started'",
		"'project_setup.status_changed'",
		"'project_setup.completed'",
		"'project_setup.failed'",
		"'failed_step', NEW.failed_step",
		"'visibility_skip_reason', NEW.visibility_skip_reason",
		// Existing trigger kept, never duplicated.
		"DROP TRIGGER IF EXISTS trg_project_setup_organization_event ON project_setup",
		"CREATE TRIGGER trg_project_setup_organization_event",
		// Unique project_id index.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_project_setup_project_id ON project_setup(project_id)",
		// Final worker job types, including the draft-missing bootstrap.
		"ai_worker_jobs_job_type_check",
		"'visibility_run'",
		"'maps_visibility'",
		"'business_profile_bootstrap'",
	})
}

// TestMigration000073IsSafeAgainstFinalState asserts the repair is idempotent
// and additive, so it is a no-op where final 000072 is already applied.
func TestMigration000073IsSafeAgainstFinalState(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000073Path))

	assertContainsAll(t, up, []string{
		"DROP CONSTRAINT IF EXISTS project_setup_failed_step_check",
		"DROP CONSTRAINT IF EXISTS project_setup_status_check",
		"DROP CONSTRAINT IF EXISTS ai_worker_jobs_job_type_check",
		"CREATE OR REPLACE FUNCTION organization_event_from_project_setup()",
		"DROP TRIGGER IF EXISTS trg_project_setup_organization_event",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_project_setup_project_id",
		// The requester FK is discovered by shape, not assumed by name.
		"FROM pg_constraint con",
	})

	// The repair must not destroy data while converging either state.
	for _, forbidden := range []string{"DROP TABLE", "DROP COLUMN", "TRUNCATE", "DELETE FROM", "DROP SCHEMA"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("repair Up must be non-destructive but contains %q", forbidden)
		}
	}
}

// TestMigration000073DownIsNonDestructive documents and enforces the
// intentionally no-op Down, which cannot know which draft state was present.
func TestMigration000073DownIsNonDestructive(t *testing.T) {
	down := migrationDownSection(t, readMigration(t, migration000073Path))

	if !strings.Contains(down, "SELECT 1") {
		t.Error("repair Down must be the documented no-op SELECT 1")
	}
	for _, forbidden := range []string{"DROP ", "DELETE FROM", "ALTER TABLE", "TRUNCATE"} {
		if strings.Contains(strings.ToUpper(down), forbidden) {
			t.Errorf("repair Down must be non-destructive but contains %q", forbidden)
		}
	}
}

// TestMigration000073ConvergesToFinal000072 asserts the repair's final schema
// contract matches the finalized 000072 source, including the trigger function
// contract, so both states converge on one schema.
func TestMigration000073ConvergesToFinal000072(t *testing.T) {
	final := readMigration(t, migration000072Path)
	repair := migrationUpSection(t, readMigration(t, migration000073Path))

	for _, shared := range []string{
		"'ready', 'crawling', 'profile_generation', 'prompt_generation', 'visibility', 'completed', 'failed'",
		"failed_step IS NULL OR failed_step IN ('crawling', 'profile_generation', 'prompt_generation', 'visibility')",
		"CREATE OR REPLACE FUNCTION organization_event_from_project_setup()",
		"RETURN NULL;\n    END IF;\n\n    v_payload := jsonb_build_object(",
		"'prompt_generation', 'visibility_run', 'maps_visibility', 'business_profile_bootstrap'",
	} {
		if !strings.Contains(final, shared) {
			t.Fatalf("final 000072 source is missing expected contract %q", shared)
		}
		if !strings.Contains(repair, shared) {
			t.Errorf("repair does not converge to final 000072 contract %q", shared)
		}
	}
}

// TestMigration000073IsLatestMarker guards the goose ordering marker: 000073
// must be present, numbered after 000072, and the highest-numbered migration.
func TestMigration000073IsLatestMarker(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	latest := ""
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		if strings.HasPrefix(name, "000073") {
			count++
		}
		if name > latest {
			latest = name
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one 000073 migration, found %d", count)
	}
	if latest != filepath.Base(migration000073Path) {
		t.Errorf("latest migration = %q, want %q", latest, filepath.Base(migration000073Path))
	}
}
