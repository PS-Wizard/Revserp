package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
)

// newMCPMigrationTestPool connects to an explicit disposable database only. It
// never falls back to DATABASE_URL, so this fixture cannot apply migration 087
// to a development or production database.
func newMCPMigrationTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("MCP_MIGRATION_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCP_MIGRATION_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("mcp migration test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

// mcpTestAlias mirrors aichattools.MCPModelToolName without importing the
// package, so the expected alias is computed independently of the SQL.
func mcpTestAlias(connectionID, remoteName string) string {
	sum := sha256.Sum256([]byte(remoteName))
	return "mcp_" + strings.ReplaceAll(strings.ToLower(connectionID), "-", "") + "_" + hex.EncodeToString(sum[:])[:16]
}

// TestMigration087ConvertsLegacyDisabledToolAliases applies the real migration
// 087 SQL to a scratch schema seeded with legacy WordPress and Rune connections
// under one organization, then asserts the retired cms__/wp__ denylist entries
// keep their admin Deny alongside the new generic aliases, credentials and
// approval history survive, and the irreversible Down still refuses to run.
func TestMigration087ConvertsLegacyDisabledToolAliases(t *testing.T) {
	pool, ctx := newMCPMigrationTestPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schema := pgx.Identifier{fmt.Sprintf("mcp_alias_%d", time.Now().UnixNano())}.Sanitize()
	for _, statement := range []string{
		"CREATE SCHEMA " + schema,
		"SET LOCAL search_path TO " + schema,
		"CREATE TABLE projects (id UUID PRIMARY KEY, organization_id UUID NOT NULL)",
		"CREATE TABLE organization_features (org_id UUID PRIMARY KEY, disabled_ai_tools TEXT[] NOT NULL DEFAULT '{}')",
		"CREATE TABLE project_cms_connections (project_id UUID PRIMARY KEY, provider TEXT NOT NULL, endpoint_url TEXT NOT NULL, encrypted_token TEXT NOT NULL, revision UUID NOT NULL, tools JSONB NOT NULL DEFAULT '[]', last_checked_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())",
		"CREATE TABLE ai_conversations (id UUID PRIMARY KEY, project_id UUID NOT NULL)",
		"CREATE TABLE ai_turns (id UUID PRIMARY KEY, conversation_id UUID NOT NULL, status TEXT NOT NULL, disabled_ai_tools TEXT[] NOT NULL DEFAULT '{}')",
		"CREATE TABLE ai_cms_approvals (id UUID PRIMARY KEY, turn_id UUID NOT NULL, tool_call_id TEXT NOT NULL, tool_name TEXT NOT NULL, provider TEXT NOT NULL, status TEXT NOT NULL)",
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("scratch schema %q: %v", statement, err)
		}
	}

	var orgID, projectWP, projectRune string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	newProject := func() string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO projects (id, organization_id) VALUES (gen_random_uuid(), $1::uuid) RETURNING id::text`, orgID).Scan(&id); err != nil {
			t.Fatalf("create project: %v", err)
		}
		return id
	}
	projectWP, projectRune = newProject(), newProject()

	if _, err := tx.Exec(ctx, `INSERT INTO organization_features (org_id, disabled_ai_tools) VALUES ($1::uuid, ARRAY['read_issues','wp__list_content','cms__update_record','cms__create_record','wpYYread','cmsXXread'])`, orgID); err != nil {
		t.Fatalf("seed organization_features: %v", err)
	}
	const revisionWP = "11111111-1111-1111-1111-111111111111"
	const revisionRune = "22222222-2222-2222-2222-222222222222"
	if _, err := tx.Exec(ctx, `INSERT INTO project_cms_connections (project_id, provider, endpoint_url, encrypted_token, revision, tools, last_checked_at) VALUES ($1::uuid,'wordpress','https://wp.example/mcp','enc:wp',$2::uuid,'[{"name":"list_content"},{"name":"update_content"}]'::jsonb, now())`, projectWP, revisionWP); err != nil {
		t.Fatalf("seed wordpress connection: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_cms_connections (project_id, provider, endpoint_url, encrypted_token, revision, tools, last_checked_at) VALUES ($1::uuid,'rune','https://rune.example/mcp','enc:rune',$2::uuid,'[{"name":"update_record"},{"name":"create_record"}]'::jsonb, now())`, projectRune, revisionRune); err != nil {
		t.Fatalf("seed rune connection: %v", err)
	}

	var conversationWP, conversationRune string
	if err := tx.QueryRow(ctx, `INSERT INTO ai_conversations (id, project_id) VALUES (gen_random_uuid(), $1::uuid) RETURNING id::text`, projectWP).Scan(&conversationWP); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO ai_conversations (id, project_id) VALUES (gen_random_uuid(), $1::uuid) RETURNING id::text`, projectRune).Scan(&conversationRune); err != nil {
		t.Fatal(err)
	}

	var turnQueued, turnRunning, turnCompleted string
	if err := tx.QueryRow(ctx, `INSERT INTO ai_turns (id, conversation_id, status, disabled_ai_tools) VALUES (gen_random_uuid(), $1::uuid, 'queued', ARRAY['wp__list_content','wpYYread']) RETURNING id::text`, conversationWP).Scan(&turnQueued); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO ai_turns (id, conversation_id, status, disabled_ai_tools) VALUES (gen_random_uuid(), $1::uuid, 'running', ARRAY['cms__update_record','cmsXXread']) RETURNING id::text`, conversationRune).Scan(&turnRunning); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO ai_turns (id, conversation_id, status, disabled_ai_tools) VALUES (gen_random_uuid(), $1::uuid, 'completed', ARRAY['wp__list_content']) RETURNING id::text`, conversationWP).Scan(&turnCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_cms_approvals (id, turn_id, tool_call_id, tool_name, provider, status) VALUES (gen_random_uuid(), $1::uuid, 'call-1', 'wp__list_content', 'wordpress', 'approved')`, turnQueued); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	migration, err := mcpMarketplaceMigrationSQL()
	if err != nil {
		t.Fatal(err)
	}
	up, err := migrationUpStatements(migration)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range up {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("apply migration 087 statement %.70q: %v", statement, err)
		}
	}

	type connection struct {
		id, name, service, endpoint, token, revision string
	}
	readConnection := func(projectID string) connection {
		var got connection
		if err := tx.QueryRow(ctx, `SELECT id::text, name, service, endpoint_url, encrypted_token, revision::text FROM project_mcp_connections WHERE project_id = $1::uuid`, projectID).Scan(&got.id, &got.name, &got.service, &got.endpoint, &got.token, &got.revision); err != nil {
			t.Fatalf("read connection: %v", err)
		}
		return got
	}
	wordpress := readConnection(projectWP)
	rune := readConnection(projectRune)
	if wordpress.name != "WordPress" || wordpress.service != "wordpress" {
		t.Errorf("wordpress connection = %+v", wordpress)
	}
	if rune.name != "Rune CMS" || rune.service != "custom" {
		t.Errorf("rune connection = %+v", rune)
	}
	if wordpress.endpoint != "https://wp.example/mcp" || wordpress.token != "enc:wp" || wordpress.revision != revisionWP {
		t.Errorf("wordpress credentials/revision not preserved: %+v", wordpress)
	}
	if rune.endpoint != "https://rune.example/mcp" || rune.token != "enc:rune" || rune.revision != revisionRune {
		t.Errorf("rune credentials/revision not preserved: %+v", rune)
	}

	var disabled []string
	if err := tx.QueryRow(ctx, `SELECT disabled_ai_tools FROM organization_features WHERE org_id = $1::uuid`, orgID).Scan(&disabled); err != nil {
		t.Fatalf("read org denylist: %v", err)
	}
	for _, wanted := range []string{
		"read_issues", "wp__list_content", "cms__update_record", "cms__create_record",
		"wpYYread", "cmsXXread",
		mcpTestAlias(wordpress.id, "list_content"),
		mcpTestAlias(rune.id, "update_record"), mcpTestAlias(rune.id, "create_record"),
	} {
		if !slices.Contains(disabled, wanted) {
			t.Errorf("org denylist missing %q: %v", wanted, disabled)
		}
	}
	for _, wrong := range []string{
		mcpTestAlias(rune.id, "list_content"), mcpTestAlias(wordpress.id, "update_record"),
		mcpTestAlias(wordpress.id, "wpYYread"), mcpTestAlias(rune.id, "cmsXXread"),
	} {
		if slices.Contains(disabled, wrong) {
			t.Errorf("org denylist cross-mapped %q: %v", wrong, disabled)
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(disabled)))) != len(disabled) {
		t.Errorf("org denylist has duplicate aliases: %v", disabled)
	}

	readTurnTools := func(turnID string) []string {
		var names []string
		if err := tx.QueryRow(ctx, `SELECT disabled_ai_tools FROM ai_turns WHERE id = $1::uuid`, turnID).Scan(&names); err != nil {
			t.Fatalf("read turn snapshot: %v", err)
		}
		return names
	}
	queued := readTurnTools(turnQueued)
	for _, wanted := range []string{"wp__list_content", "wpYYread", mcpTestAlias(wordpress.id, "list_content")} {
		if !slices.Contains(queued, wanted) {
			t.Errorf("queued turn snapshot missing %q: %v", wanted, queued)
		}
	}
	if slices.Contains(queued, mcpTestAlias(wordpress.id, "wpYYread")) {
		t.Errorf("queued turn generated alias for nonlegacy wpYYread: %v", queued)
	}
	running := readTurnTools(turnRunning)
	for _, wanted := range []string{"cms__update_record", "cmsXXread", mcpTestAlias(rune.id, "update_record")} {
		if !slices.Contains(running, wanted) {
			t.Errorf("running turn snapshot missing %q: %v", wanted, running)
		}
	}
	if slices.Contains(running, mcpTestAlias(rune.id, "cmsXXread")) {
		t.Errorf("running turn generated alias for nonlegacy cmsXXread: %v", running)
	}
	if completed := readTurnTools(turnCompleted); !slices.Equal(completed, []string{"wp__list_content"}) {
		t.Errorf("historical completed snapshot changed: %v", completed)
	}

	var approvalTool, approvalConnection string
	if err := tx.QueryRow(ctx, `SELECT tool_name, connection_id::text FROM ai_mcp_approvals WHERE turn_id = $1::uuid`, turnQueued).Scan(&approvalTool, &approvalConnection); err != nil {
		t.Fatalf("read approval history: %v", err)
	}
	if approvalTool != "wp__list_content" || approvalConnection != wordpress.id {
		t.Errorf("approval history = %q/%q, want wp__list_content/%q", approvalTool, approvalConnection, wordpress.id)
	}

	_, down, _ := strings.Cut(migration, "-- +goose Down")
	if !strings.Contains(down, "is irreversible") {
		t.Fatalf("migration 087 Down lost its irreversible guard")
	}
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "irreversible") {
		t.Errorf("migration 087 Down did not refuse to run: %v", err)
	}
}
