package localvisibility

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The 1-5 Maps query product limit is gone: a run carries every enabled query.
// These pure tests pin the estimate and the frozen-snapshot validator; the
// DB-gated tests below pin the atomic reservation and budget refusal.

func TestMapQueryCountHasNoProductCap(t *testing.T) {
	queries := distinctMapQueries(7)
	got, err := ValidateMapQueries(queries)
	if err != nil {
		t.Fatalf("ValidateMapQueries(7) returned error: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("len(got) = %d, want 7 with no truncation", len(got))
	}
	if _, err := ValidateMapQueries(nil); err == nil {
		t.Fatal("zero enabled queries must still be rejected")
	}
	// The only ceiling is the INTEGER credit storage bound, not a query count.
	atLimit, err := ExpectedRunCredits(MaxMapQueries, GridPointCount)
	if err != nil {
		t.Fatalf("ExpectedRunCredits(MaxMapQueries) returned error: %v", err)
	}
	if atLimit != MaxMapQueries*GridPointCount*MapsCreditsPerCall {
		t.Fatalf("at-limit credits = %d, want the full count", atLimit)
	}
	if int64(atLimit) > maxRunCredits {
		t.Fatalf("at-limit credits %d exceed the INTEGER bound %d", atLimit, maxRunCredits)
	}
	if _, err := ExpectedRunCredits(MaxMapQueries+1, GridPointCount); err == nil {
		t.Fatal("a count whose total overflows INTEGER credits must be rejected")
	}
}

func TestValidateLocalRunSnapshotAcceptsMoreThanFiveQueries(t *testing.T) {
	snapshot := validTestSnapshot()
	snapshot.Queries = distinctMapQueries(7)
	expected, err := ExpectedRunCredits(len(snapshot.Queries), GridPointCount)
	if err != nil {
		t.Fatalf("ExpectedRunCredits: %v", err)
	}
	if expected != 7*GridPointCount*MapsCreditsPerCall {
		t.Fatalf("expected credits = %d, want the full seven-query cost", expected)
	}
	if err := validateLocalRunSnapshot(snapshot, int32(expected)); err != nil {
		t.Fatalf("seven-query snapshot rejected: %v", err)
	}
}

func seedExtraEnabledQuery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, locationID pgtype.UUID, text string, ordinal int32) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin)
		VALUES ($1, $2, $3, $4, TRUE, 'map', 'manual', 'service')`, locationID, text, strings.ToLower(text), ordinal); err != nil {
		t.Fatalf("seed extra enabled query: %v", err)
	}
}

func TestEnqueueRunReservesEveryEnabledQueryBeyondFive(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	seedExtraEnabledQuery(t, ctx, pool, f.locationID, "lv q6 "+f.placeID, 5)

	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, false)
	store := LocalVisibilityStore{Pool: pool, MapsEndpoint: server.URL}

	const wantCredits = 6 * GridPointCount * MapsCreditsPerCall
	run, err := store.EnqueueRun(ctx, f.userID, f.projectID, f.locationID, 5000, wantCredits)
	if err != nil {
		t.Fatalf("enqueue with six queries: %v", err)
	}
	if int(run.ExpectedCredits) != wantCredits || int(run.ReservedCredits) != wantCredits {
		t.Fatalf("expected/reserved = %d/%d, want %d/%d", run.ExpectedCredits, run.ReservedCredits, wantCredits, wantCredits)
	}
	if int(run.ExpectedCredits) == 5*GridPointCount*MapsCreditsPerCall {
		t.Fatal("reservation used the old five-query product price")
	}
	cells, err := sqlc.New(pool).GetLocalRunCells(ctx, run.ID)
	if err != nil {
		t.Fatalf("cells: %v", err)
	}
	if len(cells) != 6*GridPointCount {
		t.Fatalf("cells = %d, want %d (one per enabled query per point)", len(cells), 6*GridPointCount)
	}
	if hits.Load() != 0 {
		t.Fatalf("enqueue made %d provider calls, want 0", hits.Load())
	}
}

func TestEnqueueRunBudgetRefusalBeyondFiveWritesNothing(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	seedExtraEnabledQuery(t, ctx, pool, f.locationID, "lv q6 "+f.placeID, 5)

	// Drain the organization allowance so the six-query reservation cannot be met.
	if _, err := pool.Exec(ctx, `UPDATE organization_maps_credit_budgets SET remaining_credits = 0, reserved_credits = 0
		WHERE organization_id = (SELECT organization_id FROM projects WHERE id = $1)`, f.projectID); err != nil {
		t.Fatalf("drain org budget: %v", err)
	}

	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, false)
	store := LocalVisibilityStore{Pool: pool, MapsEndpoint: server.URL}

	if _, err := store.EnqueueRun(ctx, f.userID, f.projectID, f.locationID, 5000, 6*GridPointCount*MapsCreditsPerCall); !errors.Is(err, ErrMapsBudgetUnavailable) {
		t.Fatalf("enqueue err = %v, want ErrMapsBudgetUnavailable", err)
	}

	var runs, cells int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM local_visibility_runs WHERE location_id = $1`, f.locationID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM local_run_cells c JOIN local_visibility_runs r ON r.id = c.run_id
		WHERE r.location_id = $1`, f.locationID).Scan(&cells); err != nil {
		t.Fatalf("count cells: %v", err)
	}
	if runs != 0 || cells != 0 {
		t.Fatalf("refused run wrote runs=%d cells=%d, want 0", runs, cells)
	}
	if hits.Load() != 0 {
		t.Fatalf("refused run made %d provider calls, want 0", hits.Load())
	}
	var reserved int64
	if err := pool.QueryRow(ctx, `SELECT reserved_credits FROM organization_maps_credit_budgets
		WHERE organization_id = (SELECT organization_id FROM projects WHERE id = $1)`, f.projectID).Scan(&reserved); err != nil {
		t.Fatalf("read org reserved: %v", err)
	}
	if reserved != 0 {
		t.Fatalf("refused run left %d credits reserved, want 0", reserved)
	}
}
