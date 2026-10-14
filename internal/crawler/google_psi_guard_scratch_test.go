package crawler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The worker's PSI fetch is the only writer of google_psi_results, so the
// write is gated on running: a fetch that outlives a cancel must not land on
// a terminal row. Scratch schema only, same guards as the progress roundtrip.
func TestScratchGooglePSIWriteGatedOnRunning(t *testing.T) {
	pool, ctx := crawlerScratchPool(t)
	queries := sqlc.New(pool)
	payload := []byte(`[{"url":"https://example.com/"}]`)

	runningID, cleanupRunning := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanupRunning)
	if err := queries.UpdateCrawlGooglePSIResults(ctx, sqlc.UpdateCrawlGooglePSIResultsParams{
		ID: runningID, GooglePsiResults: payload,
	}); err != nil {
		t.Fatalf("psi write on running crawl: %v", err)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT google_psi_results FROM crawls WHERE id = $1`, runningID).Scan(&stored); err != nil {
		t.Fatalf("read psi results: %v", err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("stored psi results = %s, want the payload", stored)
	}

	cancelledID, cleanupCancelled := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanupCancelled)
	var memberID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT om.user_id FROM organization_members AS om
		INNER JOIN projects AS p ON p.organization_id = om.org_id
		INNER JOIN crawls AS c ON c.project_id = p.id
		WHERE c.id = $1 LIMIT 1`, cancelledID).Scan(&memberID); err != nil {
		t.Fatalf("read member: %v", err)
	}
	if _, err := queries.CancelCrawlByIDForUser(ctx, sqlc.CancelCrawlByIDForUserParams{
		ID: cancelledID, UserID: memberID,
	}); err != nil {
		t.Fatalf("cancel crawl: %v", err)
	}
	if err := queries.UpdateCrawlGooglePSIResults(ctx, sqlc.UpdateCrawlGooglePSIResultsParams{
		ID: cancelledID, GooglePsiResults: payload,
	}); err != nil {
		t.Fatalf("late psi write must be skipped, not error: %v", err)
	}
	stored = nil
	if err := pool.QueryRow(ctx, `SELECT google_psi_results FROM crawls WHERE id = $1`, cancelledID).Scan(&stored); err != nil {
		t.Fatalf("read psi results: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("cancelled crawl psi results = %s, want NULL", stored)
	}
}
