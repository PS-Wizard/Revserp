package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Scratch-database integration checks for the location workspace migrations
// 099-105. The URL comes from LOCATION_SCRATCH_TEST_DATABASE_URL only; the
// suite refuses any non-local host or non-scratch database name and never
// falls back to a production URL. No provider/Google/Serper calls are made.
const scratchDatabaseEnv = "LOCATION_SCRATCH_TEST_DATABASE_URL"

func scratchPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	raw := os.Getenv(scratchDatabaseEnv)
	if raw == "" {
		t.Skip(scratchDatabaseEnv + " is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", scratchDatabaseEnv, err)
	}
	if name := strings.Trim(parsed.Path, "/"); !strings.HasPrefix(name, "revserp_layer_c_") {
		t.Fatalf("refusing %s database %q: want a revserp_layer_c_* scratch database", scratchDatabaseEnv, name)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("refusing %s host %q: want 127.0.0.1 or localhost", scratchDatabaseEnv, host)
	}
	ctx := context.Background()
	pool, err := Connect(ctx, raw, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("%s is not available: %v", scratchDatabaseEnv, err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

type scratchFixture struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	orgID     pgtype.UUID
	userID    pgtype.UUID
	projectID pgtype.UUID
}

func newScratchFixture(t *testing.T) *scratchFixture {
	t.Helper()
	pool, ctx := scratchPool(t)
	prefix := fmt.Sprintf("scratch-%d-%d", os.Getpid(), time.Now().UnixNano())
	fx := &scratchFixture{t: t, ctx: ctx, pool: pool}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, prefix).Scan(&fx.orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test',$1,$2) RETURNING id`,
		prefix, prefix+"@example.com").Scan(&fx.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,$2,'https://scratch.example') RETURNING id`,
		fx.orgID, prefix).Scan(&fx.projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		conn, err := pool.Acquire(bg)
		if err != nil {
			return
		}
		defer conn.Release()
		if _, err := conn.Exec(bg, `BEGIN`); err != nil {
			return
		}
		_, _ = conn.Exec(bg, `ALTER TABLE location_website_scopes DISABLE TRIGGER location_website_scope_revision_immutable`)
		_, _ = conn.Exec(bg, `ALTER TABLE project_locations DISABLE TRIGGER location_unsettled_maps_spend_guard`)
		_, _ = conn.Exec(bg, `DELETE FROM ai_conversations WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1)`, fx.orgID)
		_, _ = conn.Exec(bg, `DELETE FROM location_gsc_connections WHERE location_id IN (SELECT id FROM project_locations WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1))`, fx.orgID)
		_, _ = conn.Exec(bg, `DELETE FROM location_google_analytics_connections WHERE location_id IN (SELECT id FROM project_locations WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1))`, fx.orgID)
		_, _ = conn.Exec(bg, `DELETE FROM project_gsc_connections WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1)`, fx.orgID)
		_, _ = conn.Exec(bg, `DELETE FROM project_google_analytics_connections WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1)`, fx.orgID)
		_, _ = conn.Exec(bg, `DELETE FROM organizations WHERE id=$1`, fx.orgID)
		_, _ = conn.Exec(bg, `ALTER TABLE location_website_scopes ENABLE TRIGGER location_website_scope_revision_immutable`)
		_, _ = conn.Exec(bg, `ALTER TABLE project_locations ENABLE TRIGGER location_unsettled_maps_spend_guard`)
		_, _ = conn.Exec(bg, `COMMIT`)
		_, _ = conn.Exec(bg, `DELETE FROM users WHERE id=$1`, fx.userID)
	})
	return fx
}

func (fx *scratchFixture) newProject(name string) pgtype.UUID {
	fx.t.Helper()
	var id pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,$2,'https://scratch.example') RETURNING id`,
		fx.orgID, name).Scan(&id); err != nil {
		fx.t.Fatalf("create project %q: %v", name, err)
	}
	return id
}

func (fx *scratchFixture) newLocation(project pgtype.UUID, name string) pgtype.UUID {
	fx.t.Helper()
	var id pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,$2,27.7172,85.3240) RETURNING id`,
		project, name).Scan(&id); err != nil {
		fx.t.Fatalf("create location %q: %v", name, err)
	}
	return id
}

func expectExecError(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) error {
	t.Helper()
	_, err := pool.Exec(ctx, sql, args...)
	if err == nil {
		t.Fatalf("expected an error from: %s", sql)
	}
	return err
}

func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// 099: the copy is independent, verbatim for product_description, snapshots
// services at copy time, and never propagates later parent edits.
func TestScratchMigration099ProfileCopyIsIndependent(t *testing.T) {
	fx := newScratchFixture(t)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_business_profile
		(project_id, brand_name, website_url, product_description, seed_prompts)
		VALUES ($1,'ParentBrand','https://parent.example','VERBATIM-99','["parent prompt"]')`, fx.projectID); err != nil {
		t.Fatalf("seed parent profile: %v", err)
	}
	for _, svc := range []struct{ label, normalized string }{
		{"Zeta", "zeta"}, {"alpha", "alpha"}, {"Beta", "beta"},
	} {
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_services (project_id, label, normalized_label) VALUES ($1,$2,$3)`,
			fx.projectID, svc.label, svc.normalized); err != nil {
			t.Fatalf("seed service: %v", err)
		}
	}

	loc := fx.newLocation(fx.projectID, "copy-target")

	var brand, product, services, seed string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT brand_name, product_description, services::text, seed_prompts::text
		FROM location_business_profiles WHERE location_id=$1`, loc).Scan(&brand, &product, &services, &seed); err != nil {
		t.Fatalf("load copied profile: %v", err)
	}
	if brand != "ParentBrand" || product != "VERBATIM-99" {
		t.Fatalf("copied facts = (%q,%q), want verbatim parent facts", brand, product)
	}
	if seed != "[]" {
		t.Fatalf("seed_prompts = %s, want empty local prompts", seed)
	}
	if services != `["alpha", "Beta", "Zeta"]` {
		t.Fatalf("services snapshot = %s, want labels ordered by normalized_label", services)
	}

	if _, err := fx.pool.Exec(fx.ctx, `UPDATE project_business_profile SET brand_name='ChangedBrand', product_description='CHANGED' WHERE project_id=$1`, fx.projectID); err != nil {
		t.Fatalf("update parent profile: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_services (project_id, label, normalized_label) VALUES ($1,'New','new')`, fx.projectID); err != nil {
		t.Fatalf("add parent service: %v", err)
	}
	var afterBrand, afterProduct, afterServices string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT brand_name, product_description, services::text FROM location_business_profiles WHERE location_id=$1`, loc).Scan(&afterBrand, &afterProduct, &afterServices); err != nil {
		t.Fatalf("reload copied profile: %v", err)
	}
	if afterBrand != "ParentBrand" || afterProduct != "VERBATIM-99" || afterServices != services {
		t.Fatalf("parent edit propagated: brand=%q product=%q services=%s", afterBrand, afterProduct, afterServices)
	}

	if _, err := fx.pool.Exec(fx.ctx, `UPDATE location_business_profiles SET brand_name='LocalOnly' WHERE location_id=$1`, loc); err != nil {
		t.Fatalf("update local profile: %v", err)
	}
	var parentBrand string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT brand_name FROM project_business_profile WHERE project_id=$1`, fx.projectID).Scan(&parentBrand); err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if parentBrand != "ChangedBrand" {
		t.Fatalf("local edit changed parent brand to %q", parentBrand)
	}

	noProfileProject := fx.newProject("no-profile")
	noProfileLoc := fx.newLocation(noProfileProject, "no-parent-profile")
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM location_business_profiles WHERE location_id=$1`, noProfileLoc).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("copied rows without a parent profile = %d, want 0", count)
	}

	expectExecError(t, fx.ctx, fx.pool, `UPDATE location_business_profiles SET seed_prompts='{"not":"array"}'::jsonb WHERE location_id=$1`, loc)
}

// 100: revisions are append-only and immutable, and concurrent first writes
// serialize on the location row instead of colliding on revision 1.
func TestScratchMigration100ScopeRevisionRules(t *testing.T) {
	fx := newScratchFixture(t)
	loc := fx.newLocation(fx.projectID, "scope-target")

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match) VALUES ($1,$2,1,NULL,'none')`, fx.projectID, loc); err != nil {
		t.Fatalf("insert revision 1: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match) VALUES ($1,$2,2,'https://scratch.example/blog','subtree')`, fx.projectID, loc); err != nil {
		t.Fatalf("insert revision 2: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `UPDATE location_website_scopes SET url='https://evil.example' WHERE location_id=$1 AND revision=2`, loc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match) VALUES ($1,$2,2,'https://x.example','exact')`, fx.projectID, loc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match) VALUES ($1,$2,3,'https://x.example','none')`, fx.projectID, loc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match) VALUES ($1,$2,3,NULL,'exact')`, fx.projectID, loc)

	concurrentLoc := fx.newLocation(fx.projectID, "scope-concurrent")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := fx.pool.Begin(fx.ctx)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(fx.ctx) }()
			if _, err := tx.Exec(fx.ctx, `SELECT id FROM project_locations WHERE id=$1 FOR UPDATE`, concurrentLoc); err != nil {
				errs <- err
				return
			}
			if _, err := tx.Exec(fx.ctx, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match)
				SELECT $1, $2, COALESCE(MAX(revision),0)+1, NULL, 'none' FROM location_website_scopes WHERE location_id=$2`,
				fx.projectID, concurrentLoc); err != nil {
				errs <- err
				return
			}
			if err := tx.Commit(fx.ctx); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent first write failed: %v", err)
	}
	var revisions []int
	rows, err := fx.pool.Query(fx.ctx, `SELECT revision FROM location_website_scopes WHERE location_id=$1 ORDER BY revision`, concurrentLoc)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r int
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan revision: %v", err)
		}
		revisions = append(revisions, r)
	}
	if len(revisions) != 2 || revisions[0] != 1 || revisions[1] != 2 {
		t.Fatalf("concurrent revisions = %v, want [1 2]", revisions)
	}

	// Revisions stay append-only for direct rewrites, but the parent cascade
	// must still remove them on location/project/org deletion.
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM project_locations WHERE id=$1`, loc); err != nil {
		t.Fatalf("location delete must cascade scope revisions: %v", err)
	}
	var leftover int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM location_website_scopes WHERE location_id=$1`, loc).Scan(&leftover); err != nil {
		t.Fatalf("count scopes: %v", err)
	}
	if leftover != 0 {
		t.Fatalf("scope revisions survived the cascade: %d", leftover)
	}
}

// 101: listing-search evidence is append-only with guarded fields, and the new
// radius column carries the documented bounds.
func TestScratchMigration101ListingSearchGuards(t *testing.T) {
	fx := newScratchFixture(t)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_listing_searches (project_id, user_id, query, status, raw_response, expires_at)
		VALUES ($1,$2,'coffee','completed','{"candidates":[]}'::jsonb, now() + interval '1 hour')`, fx.projectID, fx.userID); err != nil {
		t.Fatalf("insert search evidence: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `UPDATE location_listing_searches SET query='changed' WHERE project_id=$1`, fx.projectID)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_listing_searches (project_id, user_id, query, status, raw_response, expires_at)
		VALUES ($1,$2,'   ','completed','{}'::jsonb, now())`, fx.projectID, fx.userID)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_listing_searches (project_id, user_id, query, status, raw_response, expires_at)
		VALUES ($1,$2,$3,'completed','{}'::jsonb, now())`, fx.projectID, fx.userID, strings.Repeat("x", 501))
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_listing_searches (project_id, user_id, query, status, raw_response, expires_at)
		VALUES ($1,$2,'ok','bogus','{}'::jsonb, now())`, fx.projectID, fx.userID)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_listing_searches (project_id, user_id, query, status, raw_response, expires_at)
		VALUES ($1,$2,'ok','completed','[]'::jsonb, now())`, fx.projectID, fx.userID)

	var radius int
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,'radius-default',27.7,85.3) RETURNING radius_m`, fx.projectID).Scan(&radius); err != nil {
		t.Fatalf("insert location: %v", err)
	}
	if radius != 5000 {
		t.Fatalf("default radius_m = %d, want 5000", radius)
	}
	expectExecError(t, fx.ctx, fx.pool, `UPDATE project_locations SET radius_m=999 WHERE project_id=$1`, fx.projectID)
	expectExecError(t, fx.ctx, fx.pool, `UPDATE project_locations SET radius_m=25001 WHERE project_id=$1`, fx.projectID)

	// Project deletion may still cascade the evidence (immutability blocks only UPDATE).
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM projects WHERE id=$1`, fx.projectID); err != nil {
		t.Fatalf("delete project must cascade search evidence: %v", err)
	}
	var remaining int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM location_listing_searches WHERE project_id=$1`, fx.projectID).Scan(&remaining); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("search evidence survived project delete: %d rows", remaining)
	}
}

// 102: location keywords are stored per location only, independent from the
// parent project keyword list, with the documented constraints.
func TestScratchMigration102LocationKeywordIsolation(t *testing.T) {
	fx := newScratchFixture(t)
	loc := fx.newLocation(fx.projectID, "keyword-target")

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source) VALUES ($1,'parent brand','parent brand','brand','user')`, fx.projectID); err != nil {
		t.Fatalf("seed parent keyword: %v", err)
	}
	for _, kw := range []struct{ keyword, normalized, kind, source string }{
		{"Local Brand", "local brand", "brand", "user"},
		{"selected service", "selected service", "non_brand", "selected"},
	} {
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,$2,$3,$4,$5)`,
			loc, kw.keyword, kw.normalized, kw.kind, kw.source); err != nil {
			t.Fatalf("insert location keyword: %v", err)
		}
	}
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'Local Brand','local brand','brand','user')`, loc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'x','x','bogus','user')`, loc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'x','x','brand','revserp')`, loc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'   ','   ','brand','user')`, loc)

	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM project_keywords WHERE project_id=$1`, fx.projectID); err != nil {
		t.Fatalf("delete parent keywords: %v", err)
	}
	var locCount int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM location_keywords WHERE location_id=$1`, loc).Scan(&locCount); err != nil {
		t.Fatalf("count location keywords: %v", err)
	}
	if locCount != 2 {
		t.Fatalf("location keywords after parent delete = %d, want 2 (independent)", locCount)
	}
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM project_locations WHERE id=$1`, loc); err != nil {
		t.Fatalf("delete location: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM location_keywords WHERE location_id=$1`, loc).Scan(&locCount); err != nil {
		t.Fatalf("count after location delete: %v", err)
	}
	if locCount != 0 {
		t.Fatalf("location keywords survived location delete: %d", locCount)
	}
}

// 103: the five-query smallint cap is gone, query_index stays non-negative, and
// the result FK still follows the cell rows.
func TestScratchMigration103QueryIndexRelaxation(t *testing.T) {
	fx := newScratchFixture(t)
	loc := fx.newLocation(fx.projectID, "run-target")
	var runID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits)
		VALUES ($1,'running',5000,'{"queries":["q1","q2","q3","q4","q5","q6","q7","q8"]}'::jsonb, 8, 8) RETURNING id`, loc).Scan(&runID); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_run_cells (run_id, query_index, point_index) VALUES ($1,7,0)`, runID); err != nil {
		t.Fatalf("query_index 7 must be allowed after migration 103: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO local_run_cells (run_id, query_index, point_index) VALUES ($1,-1,0)`, runID)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results (run_id, query_index, point_index, call_status, match_status, rank, credits, credit_known)
		VALUES ($1,7,0,'success_nonempty','found',1,1,true)`, runID); err != nil {
		t.Fatalf("result for query_index 7: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO local_visibility_results (run_id, query_index, point_index, call_status, match_status, rank, credits, credit_known)
		VALUES ($1,6,0,'success_nonempty','found',1,1,true)`, runID)
	// Rolling back a failed run removes its cells (budget/quote accounting stays atomic).
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM local_visibility_runs WHERE id=$1`, runID); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	var cells int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM local_run_cells WHERE run_id=$1`, runID).Scan(&cells); err != nil {
		t.Fatalf("count cells: %v", err)
	}
	if cells != 0 {
		t.Fatalf("cells survived run delete: %d", cells)
	}
}

// 104: multiple accounts per org, RESTRICT on account deletion while bound,
// location binding mode/field consistency, and unbind preserves the account.
func TestScratchMigration104GoogleBindingConstraints(t *testing.T) {
	fx := newScratchFixture(t)
	loc := fx.newLocation(fx.projectID, "google-target")

	newConnection := func(subject string) pgtype.UUID {
		var id pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO google_connections (organization_id, connected_by_user_id, google_account_email, google_account_subject, encrypted_refresh_token, scope)
			VALUES ($1,$2,$3,$4,'token','https://www.googleapis.com/auth/webmasters.readonly') RETURNING id`,
			fx.orgID, fx.userID, subject+"@example.com", subject).Scan(&id); err != nil {
			t.Fatalf("create google connection: %v", err)
		}
		return id
	}
	accA := newConnection("subject-a")
	accB := newConnection("subject-b")
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO google_connections (organization_id, connected_by_user_id, google_account_subject, encrypted_refresh_token, scope)
		VALUES ($1,$2,'subject-a','token','scope')`, fx.orgID, fx.userID)

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_gsc_connections (project_id, google_connection_id, site_url) VALUES ($1,$2,'https://scratch.example/')`, fx.projectID, accA); err != nil {
		t.Fatalf("bind project gsc: %v", err)
	}
	if code := pgErrCode(expectExecError(t, fx.ctx, fx.pool, `DELETE FROM google_connections WHERE id=$1`, accA)); code != "23503" {
		t.Fatalf("deleting a bound account code = %q, want 23503 (RESTRICT)", code)
	}

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url) VALUES ($1,'custom',$2,'https://scratch.example/blog')`, loc, accB); err != nil {
		t.Fatalf("custom location gsc: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url) VALUES ($1,'inherit',$2,'https://x.example')`, fx.newLocation(fx.projectID, "gsc-inherit-bad"), accB)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url) VALUES ($1,'custom',$2,NULL)`, fx.newLocation(fx.projectID, "gsc-custom-nosite"), accB)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_gsc_connections (location_id, mode) VALUES ($1,'bogus')`, fx.newLocation(fx.projectID, "gsc-bogus"))

	if _, err := fx.pool.Exec(fx.ctx, `UPDATE location_gsc_connections SET mode='inherit', google_connection_id=NULL, site_url=NULL, permission_level=NULL WHERE location_id=$1`, loc); err != nil {
		t.Fatalf("unbind location gsc: %v", err)
	}
	var accountRows int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM google_connections WHERE id=$1`, accB).Scan(&accountRows); err != nil {
		t.Fatalf("count account: %v", err)
	}
	if accountRows != 1 {
		t.Fatalf("unbind deleted the account row: %d", accountRows)
	}

	gaLoc := fx.newLocation(fx.projectID, "analytics-target")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_google_analytics_connections (location_id, mode, google_connection_id, property_id, property_display_name) VALUES ($1,'custom',$2,'properties/1','Main')`, gaLoc, accB); err != nil {
		t.Fatalf("custom location analytics: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_google_analytics_connections (location_id, mode, google_connection_id, property_id) VALUES ($1,'custom',$2,NULL)`, fx.newLocation(fx.projectID, "ga-custom-noprop"), accB)

	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO google_oauth_states (state_token_hash, organization_id, user_id, project_id, expires_at, purpose) VALUES ('h1',$1,$2,$3,now(),'bogus')`, fx.orgID, fx.userID, fx.projectID)
}

// 105: a conversation's location scope is fixed and consistent with its project,
// and NO ACTION keeps historical chat from being nulled by a location delete.
func TestScratchMigration105ConversationLocationIsolation(t *testing.T) {
	fx := newScratchFixture(t)
	otherProject := fx.newProject("scope-other")
	locA := fx.newLocation(fx.projectID, "chat-loc-a")
	locB := fx.newLocation(otherProject, "chat-loc-b")

	newConversation := func(project pgtype.UUID, location any, title string) error {
		_, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_conversations (project_id, created_by_user_id, title, location_id) VALUES ($1,$2,$3,$4)`,
			project, fx.userID, title, location)
		return err
	}
	if err := newConversation(fx.projectID, nil, "parent chat"); err != nil {
		t.Fatalf("parent conversation: %v", err)
	}
	if err := newConversation(fx.projectID, locA, "location chat"); err != nil {
		t.Fatalf("location conversation: %v", err)
	}
	if code := pgErrCode(newConversation(fx.projectID, locB, "mismatched chat")); code != "23503" {
		t.Fatalf("foreign location conversation code = %q, want 23503", code)
	}

	if code := pgErrCode(expectExecError(t, fx.ctx, fx.pool, `DELETE FROM project_locations WHERE id=$1`, locA)); code != "23503" {
		t.Fatalf("deleting a location with history code = %q, want 23503 (NO ACTION)", code)
	}
	var title string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT title FROM ai_conversations WHERE location_id=$1`, locA).Scan(&title); err != nil {
		t.Fatalf("history must survive the blocked delete: %v", err)
	}
	if title != "location chat" {
		t.Fatalf("history title = %q, want the original row", title)
	}
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM ai_conversations WHERE location_id=$1`, locA); err != nil {
		t.Fatalf("delete location conversation: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM project_locations WHERE id=$1`, locA); err != nil {
		t.Fatalf("delete location after history removed: %v", err)
	}
	var idx int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM pg_indexes WHERE tablename='ai_conversations' AND indexname='ai_conversations_project_location_updated_idx'`).Scan(&idx); err != nil {
		t.Fatalf("check index: %v", err)
	}
	if idx != 1 {
		t.Fatal("ai_conversations_project_location_updated_idx is missing")
	}
}

func (fx *scratchFixture) newGoogleAccount(subject string) pgtype.UUID {
	fx.t.Helper()
	var id pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO google_connections (organization_id, connected_by_user_id, google_account_email, google_account_subject, encrypted_refresh_token, scope)
		VALUES ($1,$2,$3,$4,'token','https://www.googleapis.com/auth/webmasters.readonly') RETURNING id`,
		fx.orgID, fx.userID, subject+"@example.com", subject).Scan(&id); err != nil {
		fx.t.Fatalf("create google account: %v", err)
	}
	return id
}

// Tenant offboarding: one DELETE of the organization must cascade through
// projects, locations, shared Google accounts, custom bindings and local chat
// without manual child cleanup.
func TestScratchTenantOffboardingOrgCascade(t *testing.T) {
	fx := newScratchFixture(t)
	project := fx.newProject("offboard-project")
	location := fx.newLocation(project, "offboard-location")
	account := fx.newGoogleAccount("shared-offboard")

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_gsc_connections (project_id, google_connection_id, site_url) VALUES ($1,$2,'https://scratch.example/')`, project, account); err != nil {
		t.Fatalf("project gsc binding: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_google_analytics_connections (project_id, google_connection_id, property_id, property_display_name) VALUES ($1,$2,'properties/1','Main')`, project, account); err != nil {
		t.Fatalf("project analytics binding: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url) VALUES ($1,'custom',$2,'https://scratch.example/blog')`, location, account); err != nil {
		t.Fatalf("location gsc binding: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_google_analytics_connections (location_id, mode, google_connection_id, property_id, property_display_name) VALUES ($1,'custom',$2,'properties/2','Branch')`, location, account); err != nil {
		t.Fatalf("location analytics binding: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_conversations (project_id, created_by_user_id, title, location_id) VALUES ($1,$2,'local chat',$3)`, project, fx.userID, location); err != nil {
		t.Fatalf("local conversation: %v", err)
	}

	// Fully settled call: reserved_credits = 0 and no queued/running run, so the
	// migration 091 credit guard must not block offboarding.
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits, credits_used)
		VALUES ($1,'completed',5000,'{"queries":["settled"]}'::jsonb,1,0,1)`, location); err != nil {
		t.Fatalf("settled run: %v", err)
	}

	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM organizations WHERE id=$1`, fx.orgID); err != nil {
		t.Fatalf("direct organization delete must cascade tenant offboarding: %v", err)
	}
	for _, check := range []struct {
		name  string
		query string
		arg   pgtype.UUID
	}{
		{"organization", `SELECT count(*) FROM organizations WHERE id=$1`, fx.orgID},
		{"projects", `SELECT count(*) FROM projects WHERE organization_id=$1`, fx.orgID},
		{"google accounts", `SELECT count(*) FROM google_connections WHERE organization_id=$1`, fx.orgID},
		{"conversations", `SELECT count(*) FROM ai_conversations WHERE project_id=$1`, project},
	} {
		var count int
		if err := fx.pool.QueryRow(fx.ctx, check.query, check.arg).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s rows survived org delete: %d", check.name, count)
		}
	}
}

// Direct project delete with a location-scoped conversation must let the
// sibling project cascade remove chat and location in the same statement.
func TestScratchProjectDeleteWithLocalChatCascades(t *testing.T) {
	fx := newScratchFixture(t)
	project := fx.newProject("delete-project")
	location := fx.newLocation(project, "delete-project-location")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_conversations (project_id, created_by_user_id, title, location_id) VALUES ($1,$2,'local chat',$3)`, project, fx.userID, location); err != nil {
		t.Fatalf("local conversation: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM projects WHERE id=$1`, project); err != nil {
		t.Fatalf("direct project delete with local chat must cascade: %v", err)
	}
	var conversations, locations int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_conversations WHERE project_id=$1`, project).Scan(&conversations); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM project_locations WHERE project_id=$1`, project).Scan(&locations); err != nil {
		t.Fatalf("count locations: %v", err)
	}
	if conversations != 0 || locations != 0 {
		t.Fatalf("project delete left conversations=%d locations=%d", conversations, locations)
	}
}

// 091 credit protection is intentional: a queued/running run or any reserved
// (unconfirmed) credits block deletion of the location; a fully settled run
// does not. This is protected behavior, not an offboarding regression.
func TestScratchSpendGuardProtectsUnsettledRuns(t *testing.T) {
	fx := newScratchFixture(t)
	queuedLoc := fx.newLocation(fx.projectID, "spend-queued")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits)
		VALUES ($1,'queued',5000,'{}'::jsonb,1,1)`, queuedLoc); err != nil {
		t.Fatalf("queued run: %v", err)
	}
	if code := pgErrCode(expectExecError(t, fx.ctx, fx.pool, `DELETE FROM project_locations WHERE id=$1`, queuedLoc)); code != "23503" {
		t.Fatalf("queued run delete block code = %q, want 23503", code)
	}

	reservedLoc := fx.newLocation(fx.projectID, "spend-reserved")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits, credits_used)
		VALUES ($1,'completed',5000,'{}'::jsonb,1,1,0)`, reservedLoc); err != nil {
		t.Fatalf("reserved run: %v", err)
	}
	if code := pgErrCode(expectExecError(t, fx.ctx, fx.pool, `DELETE FROM project_locations WHERE id=$1`, reservedLoc)); code != "23503" {
		t.Fatalf("reserved run delete block code = %q, want 23503", code)
	}

	settledLoc := fx.newLocation(fx.projectID, "spend-settled")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits, credits_used)
		VALUES ($1,'completed',5000,'{}'::jsonb,1,0,1)`, settledLoc); err != nil {
		t.Fatalf("settled run: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM project_locations WHERE id=$1`, settledLoc); err != nil {
		t.Fatalf("settled location delete must be allowed: %v", err)
	}
}

// 106: location AI questions are independent per location, and a location
// prompt_generation job is scoped so parent/A/B never coalesce.
func TestScratchMigration106LocationAIQuestionScope(t *testing.T) {
	fx := newScratchFixture(t)
	project := fx.projectID
	locA := fx.newLocation(project, "aiq-a")
	locB := fx.newLocation(project, "aiq-b")
	otherProject := fx.newProject("aiq-other")
	foreignLoc := fx.newLocation(otherProject, "aiq-foreign")

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_ai_questions (project_id, location_id, questions) VALUES ($1,$2,'["q1"]'::jsonb)`, project, locA); err != nil {
		t.Fatalf("insert local questions: %v", err)
	}
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_ai_questions (project_id, location_id, questions) VALUES ($1,$2,'[]'::jsonb)`, project, foreignLoc)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_ai_questions (project_id, location_id, questions) VALUES ($1,$2,'[]'::jsonb)`, project, locA)
	expectExecError(t, fx.ctx, fx.pool, `INSERT INTO location_ai_questions (project_id, location_id, questions) VALUES ($1,$2,'{}'::jsonb)`, project, locB)

	enqueue := func(location any, status string) {
		t.Helper()
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status) VALUES ('prompt_generation',$1,$2,$3)`, project, location, status); err != nil {
			t.Fatalf("enqueue %s: %v", status, err)
		}
	}
	enqueue(nil, "pending") // parent
	enqueue(locA, "pending")
	enqueue(locB, "pending") // A and B coexist independently
	if code := pgErrCode(expectExecError(t, fx.ctx, fx.pool, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status) VALUES ('prompt_generation',$1,$2,'pending')`, project, locA)); code != "23505" {
		t.Fatalf("duplicate pending local job code = %q, want 23505", code)
	}
	// A running job must also block a second enqueue: a claimed paid job is
	// protected from a second click or tab.
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE ai_worker_jobs SET status='running' WHERE project_id=$1 AND location_id=$2 AND status='pending'`, project, locA); err != nil {
		t.Fatalf("claim local job: %v", err)
	}
	if code := pgErrCode(expectExecError(t, fx.ctx, fx.pool, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status) VALUES ('prompt_generation',$1,$2,'pending')`, project, locA)); code != "23505" {
		t.Fatalf("running local job must block a second enqueue, code = %q", code)
	}
	// Completed/failed rows are not active, so regeneration is allowed.
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE ai_worker_jobs SET status='completed' WHERE project_id=$1 AND location_id=$2`, project, locA); err != nil {
		t.Fatalf("complete local job: %v", err)
	}
	enqueue(locA, "pending")
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE ai_worker_jobs SET status='failed' WHERE project_id=$1 AND location_id=$2`, project, locA); err != nil {
		t.Fatalf("fail local job: %v", err)
	}
	enqueue(locA, "pending")
	enqueue(nil, "pending") // parent duplicates stay unconstrained (pre-existing)

	var payload []byte
	if err := fx.pool.QueryRow(fx.ctx, `SELECT payload FROM organization_events WHERE event_type='prompt_generation.queued' AND project_id=$1 AND payload->>'location_id'=$2 ORDER BY created_at DESC LIMIT 1`, project, locA.String()).Scan(&payload); err != nil {
		t.Fatalf("local queued event payload: %v", err)
	}
	if !strings.Contains(string(payload), locA.String()) {
		t.Fatalf("event payload missing location_id: %s", payload)
	}

	var latest pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `SELECT id FROM ai_worker_jobs WHERE project_id=$1 AND job_type='prompt_generation' AND location_id IS NULL ORDER BY created_at DESC LIMIT 1`, project).Scan(&latest); err != nil {
		t.Fatalf("latest parent prompt job: %v", err)
	}
}

// A stale location prompt job must not fail the parent project setup; a stale
// parent prompt job still does.
func TestScratchMigration106ReclaimSkipsLocationSetup(t *testing.T) {
	fx := newScratchFixture(t)
	project := fx.projectID
	loc := fx.newLocation(project, "reclaim-loc")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_setup (organization_id, project_id, status) VALUES ($1,$2,'prompt_generation')`, fx.orgID, project); err != nil {
		t.Fatalf("seed project_setup: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status, started_at) VALUES ('prompt_generation',$1,$2,'running', now() - interval '3 hours')`, project, loc); err != nil {
		t.Fatalf("seed stale local job: %v", err)
	}

	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-2 * time.Hour), Valid: true}
	if err := sqlc.New(fx.pool).ReclaimStaleRunningAIWorkerJobs(fx.ctx, cutoff); err != nil {
		t.Fatalf("reclaim local: %v", err)
	}
	var status string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT status FROM project_setup WHERE project_id=$1`, project).Scan(&status); err != nil {
		t.Fatalf("reload setup: %v", err)
	}
	if status != "prompt_generation" {
		t.Fatalf("stale local job failed parent setup: %s", status)
	}

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, status, started_at) VALUES ('prompt_generation',$1,'running', now() - interval '3 hours')`, project); err != nil {
		t.Fatalf("seed stale parent job: %v", err)
	}
	if err := sqlc.New(fx.pool).ReclaimStaleRunningAIWorkerJobs(fx.ctx, cutoff); err != nil {
		t.Fatalf("reclaim parent: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT status FROM project_setup WHERE project_id=$1`, project).Scan(&status); err != nil {
		t.Fatalf("reload setup: %v", err)
	}
	if status != "failed" {
		t.Fatalf("stale parent job must fail setup, got %s", status)
	}
}
