package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const mcpMarketplaceMigrationFile = "000087_mcp_marketplace.sql"

// mcpMarketplaceMigrationSQL reads the marketplace migration from the
// backend migrations directory, resolved from this source file so test
// binaries pass any working directory.
func mcpMarketplaceMigrationSQL() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate mcp marketplace migration")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", mcpMarketplaceMigrationFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read mcp marketplace migration: %w", err)
	}
	return string(raw), nil
}

// EnsureMCPMarketplaceTestSchema applies migration 087 (MCP marketplace
// tables) to a test database provisioned at the historical schema, then
// records goose version 87 so a later full migrate stays consistent. It is a
// no-op when the new tables already exist. Test fixtures only: production
// migrates through cmd/migrate, never through this helper.
func EnsureMCPMarketplaceTestSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var connections, approvals string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(to_regclass('public.project_mcp_connections')::text, ''), COALESCE(to_regclass('public.ai_mcp_approvals')::text, '')`).Scan(&connections, &approvals); err != nil {
		return fmt.Errorf("check mcp marketplace schema: %w", err)
	}
	if connections != "" && approvals != "" {
		return nil
	}
	migration, err := mcpMarketplaceMigrationSQL()
	if err != nil {
		return err
	}
	up, err := migrationUpStatements(migration)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin mcp marketplace schema: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('mcp-marketplace-test-schema'))`); err != nil {
		return fmt.Errorf("lock mcp marketplace schema: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(to_regclass('public.project_mcp_connections')::text, '')`).Scan(&connections); err != nil {
		return fmt.Errorf("recheck mcp marketplace schema: %w", err)
	}
	if connections == "" {
		for _, statement := range up {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return fmt.Errorf("apply mcp marketplace schema: %w", err)
			}
		}
		var goose string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(to_regclass('public.goose_db_version')::text, '')`).Scan(&goose); err != nil {
			return fmt.Errorf("check goose version table: %w", err)
		}
		if goose != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO goose_db_version(version_id, is_applied) VALUES(87, true) ON CONFLICT DO NOTHING`); err != nil {
				return fmt.Errorf("record goose version 87: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mcp marketplace schema: %w", err)
	}
	return nil
}

// migrationUpStatements extracts the executable statements before the
// +goose Down marker, dropping comment lines (which may carry semicolons).
// Statements must not contain semicolons inside string literals.
func migrationUpStatements(migration string) ([]string, error) {
	up, _, _ := strings.Cut(migration, "-- +goose Down")
	lines := make([]string, 0)
	for _, line := range strings.Split(up, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		lines = append(lines, line)
	}
	parts := strings.Split(strings.Join(lines, "\n"), ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no statements in mcp marketplace migration")
	}
	return out, nil
}
