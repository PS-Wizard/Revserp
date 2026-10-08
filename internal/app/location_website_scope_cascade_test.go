package app

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scratch checks for the revisioned website scope table. They run only
// against the disposable scratch database named by
// LOCATION_SCRATCH_TEST_DATABASE_URL after main reapplies migrations;
// without it the fixture skips. The 091 Maps spend guard is intentionally
// out of scope here: fresh locations carry no spend rows, and
// active/unconfirmed spend keeps protecting credits by design.
func insertScopeRevision(t *testing.T, pool *pgxpool.Pool, ctx context.Context, projectID, locationID pgtype.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO location_website_scopes
		(project_id, location_id, revision, url, match) VALUES ($1,$2,1,NULL,'none')`,
		projectID, locationID); err != nil {
		t.Fatalf("insert scope revision: %v", err)
	}
}

func assertNoScopes(t *testing.T, pool *pgxpool.Pool, ctx context.Context, locationID pgtype.UUID) {
	t.Helper()
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM location_website_scopes WHERE location_id=$1`, locationID).Scan(&revisions); err != nil {
		t.Fatalf("count scopes: %v", err)
	}
	if revisions != 0 {
		t.Fatalf("cascade must clean scopes: got %d", revisions)
	}
}

func TestScratchScopeDirectWritesBlocked(t *testing.T) {
	_, pool, ctx, _, _, projectID, locationID := scratchAppFixture(t)
	insertScopeRevision(t, pool, ctx, projectID, locationID)
	if _, err := pool.Exec(ctx, `UPDATE location_website_scopes SET match='exact' WHERE location_id=$1`, locationID); err == nil {
		t.Fatal("direct scope UPDATE must be rejected")
	} else if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("UPDATE must fail as immutable, got: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM location_website_scopes WHERE location_id=$1`, locationID); err == nil {
		t.Fatal("direct scope DELETE with a living location must be rejected")
	} else if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("DELETE must fail as immutable, got: %v", err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM location_website_scopes WHERE location_id=$1`, locationID).Scan(&revisions); err != nil {
		t.Fatalf("count scopes: %v", err)
	}
	if revisions != 1 {
		t.Fatalf("blocked writes must leave the revision: got %d", revisions)
	}
}

func TestScratchScopeLocationCascadeCleansScopes(t *testing.T) {
	_, pool, ctx, _, _, projectID, locationID := scratchAppFixture(t)
	insertScopeRevision(t, pool, ctx, projectID, locationID)
	if _, err := pool.Exec(ctx, `DELETE FROM project_locations WHERE id=$1`, locationID); err != nil {
		t.Fatalf("location delete must cascade through scopes: %v", err)
	}
	assertNoScopes(t, pool, ctx, locationID)
}

func TestScratchScopeProjectCascadeCleansScopes(t *testing.T) {
	_, pool, ctx, _, _, projectID, locationID := scratchAppFixture(t)
	insertScopeRevision(t, pool, ctx, projectID, locationID)
	if _, err := pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, projectID); err != nil {
		t.Fatalf("project delete must cascade through scopes: %v", err)
	}
	assertNoScopes(t, pool, ctx, locationID)
}

func TestScratchScopeOrgCascadeCleansScopes(t *testing.T) {
	_, pool, ctx, orgID, _, projectID, locationID := scratchAppFixture(t)
	insertScopeRevision(t, pool, ctx, projectID, locationID)
	if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, orgID); err != nil {
		t.Fatalf("org delete must cascade through scopes: %v", err)
	}
	assertNoScopes(t, pool, ctx, locationID)
}
