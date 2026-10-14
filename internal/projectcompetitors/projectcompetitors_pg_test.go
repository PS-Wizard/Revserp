package projectcompetitors

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestSaveProjectCompetitorRetryCommitsInRealTransaction(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Skipf("TEST_DATABASE_URL does not parse: %v", err)
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "", "localhost", "127.0.0.1", "::1":
	default:
		t.Skipf("TEST_DATABASE_URL host %q is not a local disposable database", parsed.Hostname())
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)

	var present bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'project_competitors')`).Scan(&present); err != nil || !present {
		t.Skip("project_competitors table is absent")
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'organization_features' AND column_name = 'max_competitors')`).Scan(&present); err != nil || !present {
		t.Skip("organization_features.max_competitors is absent")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	committed := false
	t.Cleanup(func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	})

	var orgID, userID, projectID pgtype.UUID
	subject := fmt.Sprintf("competitor-retry-test-%d", time.Now().UnixNano())
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('competitor-retry-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create test organization: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, 'competitor-retry-test@example.com') RETURNING id`, subject).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatalf("create test membership: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'competitor-retry-test', 'http://9.9.9.9/') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create test project: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organization_features (org_id, max_competitors) VALUES ($1, 2) ON CONFLICT (org_id) DO UPDATE SET max_competitors = 2`, orgID); err != nil {
		t.Fatalf("set test cap: %v", err)
	}

	queries := sqlc.New(pool).WithTx(tx)
	first, err := SaveProjectCompetitor(ctx, queries, projectID, userID, "Rival", "https://8.8.8.8/")
	if err != nil {
		t.Fatalf("fresh save: %v", err)
	}
	if first.Existed {
		t.Fatal("fresh save must not report existed")
	}
	belowCap, err := SaveProjectCompetitor(ctx, queries, projectID, userID, "Rival", "https://8.8.8.8/")
	if err != nil {
		t.Fatalf("retry below capacity: %v", err)
	}
	if !belowCap.Existed || belowCap.Competitor.ID != first.Competitor.ID {
		t.Fatalf("retry below capacity = %+v, want the existing row", belowCap)
	}
	if belowCap.Total != 1 {
		t.Fatalf("retry below capacity total = %d, want actual count 1", belowCap.Total)
	}
	second, err := SaveProjectCompetitor(ctx, queries, projectID, userID, "Second", "https://9.9.9.10/")
	if err != nil {
		t.Fatalf("second fresh save: %v", err)
	}
	if second.Existed {
		t.Fatal("second fresh save must not report existed")
	}
	atCap, err := SaveProjectCompetitor(ctx, queries, projectID, userID, "Second", "https://9.9.9.10/")
	if err != nil {
		t.Fatalf("retry at full capacity: %v", err)
	}
	if !atCap.Existed || atCap.Competitor.ID != second.Competitor.ID {
		t.Fatalf("retry at full capacity = %+v, want the existing row", atCap)
	}
	if atCap.Total != 2 {
		t.Fatalf("retry at full capacity total = %d, want actual count 2", atCap.Total)
	}
	var stored int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_competitors WHERE project_id = $1`, projectID).Scan(&stored); err != nil {
		t.Fatalf("count stored rows: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored rows = %d, want 2 with zero duplicates", stored)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit after retries: %v (transaction must stay healthy)", err)
	}
	committed = true

	cleanupCtx := context.Background()
	for _, stmt := range []struct {
		sql string
		arg pgtype.UUID
	}{
		{`DELETE FROM projects WHERE id = $1`, projectID},
		{`DELETE FROM organizations WHERE id = $1`, orgID},
		{`DELETE FROM users WHERE id = $1`, userID},
	} {
		if _, err := pool.Exec(cleanupCtx, stmt.sql, stmt.arg); err != nil {
			t.Errorf("cleanup %q: %v", stmt.sql, err)
		}
	}
}
