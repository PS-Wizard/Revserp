package db

import (
	"path/filepath"
	"strings"
	"testing"
)

func migration000098Path(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob("../../migrations/000098_*.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one 000098 migration, got %v (%v)", matches, err)
	}
	return matches[0]
}

func TestMigration000098AddsNullableLocationAndBranch(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000098Path(t)))
	assertContainsAll(t, up, []string{
		"ALTER TABLE ai_audits ADD COLUMN location_id UUID;",
		"ALTER TABLE ai_audit_runs ADD COLUMN mentioned_branch BOOLEAN;",
		"ALTER TABLE ai_audits ADD CONSTRAINT ai_audits_location_id_project_id_fkey",
		"FOREIGN KEY (location_id, project_id)",
		"REFERENCES project_locations(id, project_id);",
	})
	for _, forbidden := range []string{"location_id UUID NOT NULL", "mentioned_branch BOOLEAN NOT NULL", "DEFAULT"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000098 up must leave new columns nullable/unknown; found %q", forbidden)
		}
	}
	if strings.Contains(up, "ON DELETE CASCADE") {
		t.Error("000098 location FK must not cascade-delete audit history")
	}
}

func TestMigration000098ReplacesActiveIndexesByScope(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000098Path(t)))
	assertContainsAll(t, up, []string{
		"DROP INDEX IF EXISTS idx_ai_audits_one_active_per_crawl;",
		"CREATE UNIQUE INDEX idx_ai_audits_one_active_per_crawl",
		"CREATE UNIQUE INDEX idx_ai_audits_one_active_per_location",
		"ON ai_audits (project_id, location_id)",
		"AND crawl_id IS NOT NULL",
		"AND location_id IS NULL;",
		"AND location_id IS NOT NULL;",
	})
}

func TestMigration000098RewritesNoHistory(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000098Path(t)))
	for _, forbidden := range []string{"UPDATE ", "INSERT ", "DELETE ", "TRUNCATE", "DROP TABLE", "DROP COLUMN"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000098 up must be additive only; found %q", forbidden)
		}
	}
}

func TestMigration000098DownRefusesWhenHistoryExists(t *testing.T) {
	down := migrationDownSection(t, readMigration(t, migration000098Path(t)))
	assertContainsAll(t, down, []string{
		"IF EXISTS (SELECT 1 FROM ai_audits WHERE location_id IS NOT NULL)",
		"EXISTS (SELECT 1 FROM ai_audit_runs WHERE mentioned_branch IS NOT NULL)",
		"RAISE EXCEPTION 'location audits or branch evidence exist; rollback would destroy history'",
		"ALTER TABLE ai_audit_runs DROP COLUMN IF EXISTS mentioned_branch;",
		"ALTER TABLE ai_audits DROP CONSTRAINT IF EXISTS ai_audits_location_id_project_id_fkey;",
		"ALTER TABLE ai_audits DROP COLUMN IF EXISTS location_id;",
	})
	if !strings.Contains(down, "ON ai_audits (project_id, crawl_id)") || strings.Contains(down, "location_id IS NULL") {
		t.Error("000098 down must restore the un-scoped one-active-per-crawl index")
	}
}
