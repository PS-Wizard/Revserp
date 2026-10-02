package db

import (
	"strings"
	"testing"
)

// CMS migration file layout: these assertions run without a database.
const (
	migration000082Path = "../../migrations/000082_project_rune_connections.sql"
	migration000084Path = "../../migrations/000084_project_cms_connections.sql"
	migration000085Path = "../../migrations/000085_ai_cms_approvals.sql"
)

// TestMigration082RuneStaysUnchanged guards the historical Rune migration:
// the forward migration must preserve it byte-for-byte in contract.
func TestMigration082RuneStaysUnchanged(t *testing.T) {
	sql := readMigration(t, migration000082Path)
	up := migrationUpSection(t, sql)
	for _, needle := range []string{
		"CREATE TABLE project_rune_connections",
		"encrypted_token TEXT NOT NULL",
		"revision UUID NOT NULL DEFAULT gen_random_uuid()",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("migration 82 up missing %q", needle)
		}
	}
	if strings.Contains(sql, "provider") {
		t.Error("migration 82 must not mention provider; it stays Rune-only")
	}
}

// TestMigration084RenamesAndTagsProvider checks the forward migration
// preserves credentials/revision and backfills provider to rune.
func TestMigration084RenamesAndTagsProvider(t *testing.T) {
	sql := readMigration(t, migration000084Path)
	up := migrationUpSection(t, sql)
	for _, needle := range []string{
		"ALTER TABLE project_rune_connections RENAME TO project_cms_connections",
		"ADD COLUMN provider TEXT NOT NULL DEFAULT 'rune'",
		"CHECK (provider IN ('rune', 'wordpress'))",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("migration 84 up missing %q", needle)
		}
	}
	down := migrationDownSection(t, sql)
	for _, needle := range []string{
		"DROP COLUMN provider",
		"RENAME TO project_rune_connections",
	} {
		if !strings.Contains(down, needle) {
			t.Errorf("migration 84 down missing %q", needle)
		}
	}
}

// TestMigration085ApprovalContract checks the durable-approval migration
// carries waiting_for_user through every CHECK and partial index it touches.
func TestMigration085ApprovalContract(t *testing.T) {
	sql := readMigration(t, migration000085Path)
	up := migrationUpSection(t, sql)
	for _, needle := range []string{
		"'waiting_for_user'",
		"CREATE TABLE ai_cms_approvals",
		"CREATE TABLE ai_turn_checkpoints",
		"approval_required",
		"approval_decided",
		"UNIQUE (turn_id, tool_call_id)",
		"proposed_args JSONB NOT NULL",
		"connection_revision UUID",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("migration 85 up missing %q", needle)
		}
	}
	if count := strings.Count(up, "waiting_for_user"); count < 3 {
		t.Errorf("waiting_for_user appears %d times, want status CHECK plus both partial indexes", count)
	}
	down := migrationDownSection(t, sql)
	for _, needle := range []string{
		"DROP TABLE IF EXISTS ai_turn_checkpoints",
		"DROP TABLE IF EXISTS ai_cms_approvals",
	} {
		if !strings.Contains(down, needle) {
			t.Errorf("migration 85 down missing %q", needle)
		}
	}
	// The legacy waiting state stays allowed wherever migration 46 allowed it.
	if !strings.Contains(up, "'waiting', 'waiting_for_user'") && !strings.Contains(up, "'waiting','waiting_for_user'") {
		t.Error("migration 85 must keep legacy 'waiting' alongside 'waiting_for_user'")
	}
}
