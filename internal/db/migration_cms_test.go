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
	migration000087Path = "../../migrations/000087_mcp_marketplace.sql"
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

// TestMigration087MarketplaceContract checks the marketplace migration
// creates the multi-connection storage, preserves credentials/history, and
// removes the old single-connection table without touching history.
func TestMigration087MarketplaceContract(t *testing.T) {
	sql := readMigration(t, migration000087Path)
	up := migrationUpSection(t, sql)
	for _, needle := range []string{
		"CREATE TABLE project_mcp_connections",
		"CREATE TABLE project_mcp_tool_permissions",
		"CHECK (service IN ('wordpress', 'custom'))",
		"CHECK (permission IN ('ask', 'allow', 'deny'))",
		"PRIMARY KEY (connection_id, tool_name)",
		"FROM project_cms_connections",
		"ALTER TABLE ai_cms_approvals RENAME TO ai_mcp_approvals",
		"ADD COLUMN connection_id UUID",
		"ADD COLUMN remote_tool_name TEXT NOT NULL",
		"ADD COLUMN schema_digest TEXT NOT NULL",
		"UPDATE organization_features AS f",
		"UPDATE ai_turns AS t",
		"encode(sha256(convert_to(regexp_replace(a.name, '^(cms__|wp__)', ''), 'UTF8')), 'hex')",
		"left(a.name, 5) = 'cms__'",
		"left(a.name, 4) = 'wp__'",
		"t2.status IN ('queued', 'running', 'waiting', 'waiting_for_user')",
		"a.name NOT IN ('cms__', 'wp__')",
		"DROP TABLE project_cms_connections",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("migration 87 up missing %q", needle)
		}
	}
	// Rune rows become custom connections named Rune CMS; wordpress stays.
	for _, needle := range []string{
		"WHEN provider = 'rune' THEN 'Rune CMS'",
		"WHEN provider = 'rune' THEN 'custom' ELSE 'wordpress'",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("migration 87 up missing credential mapping %q", needle)
		}
	}
	// The revision column migrates explicitly so guards keep working.
	if !strings.Contains(up, "encrypted_token, revision,") {
		t.Error("migration 87 must preserve encrypted_token and revision")
	}
	// The Down direction is an explicit irreversible guard, never a silent
	// destructive rollback: multiple connections cannot fit the legacy
	// single-connection table.
	down := migrationDownSection(t, sql)
	for _, needle := range []string{
		"RAISE EXCEPTION",
		"is irreversible",
	} {
		if !strings.Contains(down, needle) {
			t.Errorf("migration 87 down missing guard %q", needle)
		}
	}
	for _, needle := range []string{
		"DROP TABLE IF EXISTS project_mcp_connections",
		"RENAME TO ai_cms_approvals",
	} {
		if strings.Contains(down, needle) {
			t.Errorf("migration 87 down must not silently destroy data (%q)", needle)
		}
	}
}

// TestMigrationUpStatementsParse pins the fixture statement splitter to the
// real migration file: every executable up statement exactly once, no
// comment fragments.
func TestMigrationUpStatementsParse(t *testing.T) {
	raw, err := mcpMarketplaceMigrationSQL()
	if err != nil {
		t.Fatal(err)
	}
	statements, err := migrationUpStatements(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 13 {
		t.Fatalf("statements = %d, want the 13 executable up statements", len(statements))
	}
	if !strings.HasPrefix(statements[0], "CREATE TABLE project_mcp_connections") {
		t.Errorf("first statement = %.60q", statements[0])
	}
	if statements[len(statements)-1] != "DROP TABLE project_cms_connections" {
		t.Errorf("last statement = %q", statements[len(statements)-1])
	}
	for i, statement := range statements {
		if strings.Contains(statement, "--") {
			t.Errorf("statement %d carries a comment fragment: %.60q", i, statement)
		}
	}
}
