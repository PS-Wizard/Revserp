package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// aiAuditHistoryFixture wires real DB-backed queries plus the
// org/project/crawl rows an audit needs. Skipped when no test database is
// available.
func aiAuditHistoryFixture(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	name := fmt.Sprintf("ai-audit-history-test-%d", time.Now().UnixNano())
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'ai-audit-history-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	return queries, pool, ctx, projectID, crawlID, orgID
}

func createAIAuditForHistory(t *testing.T, queries *sqlc.Queries, ctx context.Context, projectID, crawlID pgtype.UUID, status string) {
	t.Helper()
	if _, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID,
		CrawlID:   crawlID,
		Status:    status,
	}); err != nil {
		t.Fatalf("create %s audit: %v", status, err)
	}
}

// A terminal audit must not block a later run: history is append-only.
func TestAIAuditRerunAfterTerminalAuditAllowed(t *testing.T) {
	queries, pool, ctx, projectID, crawlID, _ := aiAuditHistoryFixture(t)
	for _, terminal := range []string{"completed", "completed_with_failures", "failed"} {
		createAIAuditForHistory(t, queries, ctx, projectID, crawlID, terminal)
		rerun, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
			ProjectID: projectID,
			CrawlID:   crawlID,
			Status:    "queued",
		})
		if err != nil {
			t.Fatalf("rerun after %s audit: %v", terminal, err)
		}
		// Close the rerun so the next iteration starts without an active audit.
		if _, err := pool.Exec(ctx, `UPDATE ai_audits SET status = 'completed' WHERE id = $1`, rerun.ID); err != nil {
			t.Fatalf("close rerun: %v", err)
		}
		if _, err := queries.GetAIAuditByCrawlAndProject(ctx, sqlc.GetAIAuditByCrawlAndProjectParams{
			ProjectID: projectID,
			CrawlID:   crawlID,
		}); err != nil {
			t.Fatalf("latest audit lookup: %v", err)
		}
	}
}

// At most one queued/running audit may exist per (project_id, crawl_id).
func TestAIAuditOnlyOneActiveAuditPerCrawl(t *testing.T) {
	queries, pool, ctx, projectID, crawlID, _ := aiAuditHistoryFixture(t)
	createAIAuditForHistory(t, queries, ctx, projectID, crawlID, "queued")
	if _, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID,
		CrawlID:   crawlID,
		Status:    "queued",
	}); !isAIAuditActiveConflictError(err) {
		t.Fatalf("second queued audit err = %v, want unique conflict", err)
	}
	if _, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID,
		CrawlID:   crawlID,
		Status:    "running",
	}); !isAIAuditActiveConflictError(err) {
		t.Fatalf("running audit while queued err = %v, want unique conflict", err)
	}

	var otherCrawl pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, projectID).Scan(&otherCrawl); err != nil {
		t.Fatalf("create other crawl: %v", err)
	}
	if _, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID,
		CrawlID:   otherCrawl,
		Status:    "queued",
	}); err != nil {
		t.Fatalf("audit for a different crawl: %v", err)
	}
}

// A rejected insert must roll back with the transaction, so the quota
// reservation made earlier in the same transaction is not consumed.
func TestAIAuditQuotaReservationRolledBackOnConflict(t *testing.T) {
	queries, pool, ctx, projectID, crawlID, orgID := aiAuditHistoryFixture(t)
	createAIAuditForHistory(t, queries, ctx, projectID, crawlID, "queued")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txQueries := queries.WithTx(tx)
	if _, err := txQueries.ReserveAIWorkspaceMonthlyAudit(ctx, sqlc.ReserveAIWorkspaceMonthlyAuditParams{
		OrganizationID: orgID,
		MonthlyLimit:   10,
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := txQueries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID,
		CrawlID:   crawlID,
		Status:    "queued",
	}); !isAIAuditActiveConflictError(err) {
		t.Fatalf("duplicate insert err = %v, want unique conflict", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var used int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ai_workspace_monthly_usage WHERE organization_id = $1`, orgID).Scan(&used); err != nil {
		t.Fatalf("count usage: %v", err)
	}
	if used != 0 {
		t.Fatalf("usage rows = %d, want 0 after rollback", used)
	}
}

func TestIsAIAuditActiveConflictError(t *testing.T) {
	if !isAIAuditActiveConflictError(&pgconn.PgError{Code: "23505"}) {
		t.Error("23505 must map to an active-audit conflict")
	}
	if isAIAuditActiveConflictError(&pgconn.PgError{Code: "23503"}) {
		t.Error("23503 must not map to an active-audit conflict")
	}
	if isAIAuditActiveConflictError(nil) {
		t.Error("nil must not map to an active-audit conflict")
	}
}
