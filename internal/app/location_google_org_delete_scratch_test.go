package app

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestScratchOrgDeleteCascadesLocationGoogleBindings proves migration 104 uses
// NO ACTION (not RESTRICT) on every google_connection_id FK: one direct org
// DELETE cascades through projects, locations, custom bindings and the shared
// account, while a direct shared-account delete with live bindings stays
// blocked and revoke preserves every row. Scratch only: skips without
// LOCATION_SCRATCH_TEST_DATABASE_URL and never touches the application DB.
func TestScratchOrgDeleteCascadesLocationGoogleBindings(t *testing.T) {
	_, pool, ctx, orgID, userID, projectID, locationID := scratchAppFixture(t)

	var accountID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO google_connections (organization_id, connected_by_user_id, encrypted_refresh_token, scope)
		VALUES ($1, $2, 'scratch-refresh', 'https://www.googleapis.com/auth/webmasters.readonly https://www.googleapis.com/auth/analytics.readonly')
		RETURNING id`, orgID, userID).Scan(&accountID); err != nil {
		t.Fatalf("create shared account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_gsc_connections (project_id, google_connection_id, site_url)
		VALUES ($1, $2, 'https://scratch.example/')`, projectID, accountID); err != nil {
		t.Fatalf("create project GSC binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_google_analytics_connections (project_id, google_connection_id, property_id, property_display_name)
		VALUES ($1, $2, '111', 'Scratch')`, projectID, accountID); err != nil {
		t.Fatalf("create project analytics binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url)
		VALUES ($1, 'custom', $2, 'https://scratch.example/branch')`, locationID, accountID); err != nil {
		t.Fatalf("create location custom GSC binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_google_analytics_connections (location_id, mode, google_connection_id, property_id)
		VALUES ($1, 'custom', $2, '111')`, locationID, accountID); err != nil {
		t.Fatalf("create location custom analytics binding: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM google_connections WHERE id = $1`, accountID); err == nil {
		t.Fatal("direct shared-account delete with live bindings must stay blocked")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("blocked delete should raise 23503, got: %v", err)
		}
	}

	if err := revokeGoogleAccountConnection(ctx, pool, accountID, "scratch revoke"); err != nil {
		t.Fatalf("revoke account: %v", err)
	}
	for _, table := range []struct {
		name string
		sql  string
		arg  any
	}{
		{"project_gsc_connections", `SELECT count(*) FROM project_gsc_connections WHERE project_id = $1`, projectID},
		{"project_google_analytics_connections", `SELECT count(*) FROM project_google_analytics_connections WHERE project_id = $1`, projectID},
		{"location_gsc_connections", `SELECT count(*) FROM location_gsc_connections WHERE location_id = $1`, locationID},
		{"location_google_analytics_connections", `SELECT count(*) FROM location_google_analytics_connections WHERE location_id = $1`, locationID},
	} {
		var count int
		if err := pool.QueryRow(ctx, table.sql, table.arg).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table.name, err)
		}
		if count != 1 {
			t.Fatalf("%s rows after revoke = %d, want 1 (revoke deletes nothing)", table.name, count)
		}
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM google_connections WHERE id = $1`, accountID).Scan(&status); err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if status != "revoked" {
		t.Fatalf("account status = %q, want revoked", status)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("direct org delete must cascade through bindings and accounts: %v", err)
	}
	for _, query := range []struct {
		name string
		sql  string
		arg  any
		want int
	}{
		{"accounts", `SELECT count(*) FROM google_connections WHERE organization_id = $1`, orgID, 0},
		{"project gsc", `SELECT count(*) FROM project_gsc_connections WHERE project_id = $1`, projectID, 0},
		{"project analytics", `SELECT count(*) FROM project_google_analytics_connections WHERE project_id = $1`, projectID, 0},
		{"location gsc", `SELECT count(*) FROM location_gsc_connections WHERE location_id = $1`, locationID, 0},
		{"location analytics", `SELECT count(*) FROM location_google_analytics_connections WHERE location_id = $1`, locationID, 0},
	} {
		var count int
		if err := pool.QueryRow(ctx, query.sql, query.arg).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		if count != query.want {
			t.Fatalf("%s rows after org delete = %d, want %d", query.name, count, query.want)
		}
	}
}
