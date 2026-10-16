package locationsetup

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// DB-backed setup tests run only against the disposable database named by
// LOCAL_SEO_TEST_DATABASE_URL and skip otherwise, mirroring the local
// visibility handler tests. They spend no platform Maps credits: every Maps
// path under test ends in a skip, and AI quota assertions stay on the
// fixture organization.

type setupFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	orgID     pgtype.UUID
	projectID pgtype.UUID
	userID    pgtype.UUID
}

func newSetupFixture(t *testing.T) setupFixture {
	t.Helper()
	databaseURL := os.Getenv("LOCAL_SEO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LOCAL_SEO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("setup test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var regclass string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.location_setup')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("location_setup is not migrated in the test database")
	}
	fx := setupFixture{ctx: ctx, pool: pool}
	name := t.Name()
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&fx.orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('location-setup-test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&fx.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
		fx.orgID, fx.userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'setup-test', 'https://setup-test.example') RETURNING id`, fx.orgID).Scan(&fx.projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM ai_worker_jobs WHERE project_id = $1`, fx.projectID)
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE id = $1`, fx.projectID)
		_, _ = pool.Exec(cleanup, `DELETE FROM organizations WHERE id = $1`, fx.orgID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, fx.userID)
	})
	return fx
}

func (fx setupFixture) newLocation(t *testing.T, placeID string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_locations
		(project_id, name, place_id, latitude, longitude, address, locality, localities)
		VALUES ($1, 'loc', $2, 27.7, 85.3, 'addr', 'Testville', '["Testville"]')
		RETURNING id`, fx.projectID, placeID).Scan(&id); err != nil {
		t.Fatalf("create location: %v", err)
	}
	return id
}

func (fx setupFixture) platformReserved(t *testing.T) int64 {
	t.Helper()
	var reserved int64
	if err := fx.pool.QueryRow(fx.ctx, `SELECT reserved_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(&reserved); err != nil {
		t.Fatalf("read platform budget: %v", err)
	}
	return reserved
}

func TestLaunchUnfinishedIsIdempotent(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-idem")
	tx, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, _, err := StartOrResume(fx.ctx, tx, fx.projectID, locationID, fx.userID, ""); err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("start: %v", err)
	}
	if err := tx.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for range 2 {
		if err := LaunchUnfinished(fx.ctx, fx.pool, fx.projectID, locationID, "http://127.0.0.1:1/maps"); err != nil {
			t.Fatalf("launch: %v", err)
		}
	}
	var jobs int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM ai_worker_jobs
		WHERE project_id = $1 AND location_id = $2 AND job_type = 'location_profile_generation'`,
		fx.projectID, locationID).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 1 {
		t.Fatalf("profile jobs = %d, want 1", jobs)
	}
}

func TestMapsLaunchSkipsWithoutBudgetSpend(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-budget")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_location_queries
		(location_id, text, normalized, ordinal, enabled, kind, source, origin)
		VALUES ($1, 'momo in Testville', 'momo in testville', 0, TRUE, 'map', 'generated', 'service')`, locationID); err != nil {
		t.Fatalf("seed query: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE organization_maps_credit_budgets
		SET remaining_credits = 0, reserved_credits = 0 WHERE organization_id = $1`, fx.orgID); err != nil {
		t.Fatalf("exhaust budget: %v", err)
	}
	tx, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	setup, _, err := StartOrResume(fx.ctx, tx, fx.projectID, locationID, fx.userID, "")
	if err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("start: %v", err)
	}
	setup.Steps[StepProfile] = StepCompleted
	setup.Steps[StepMapQueries] = StepCompleted
	if _, err := tx.Exec(fx.ctx, `UPDATE location_setup SET profile_status = 'completed',
		map_queries_status = 'completed' WHERE project_id = $1 AND location_id = $2`,
		fx.projectID, locationID); err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("advance: %v", err)
	}
	_ = setup
	if err := tx.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	before := fx.platformReserved(t)
	if err := LaunchMapsVisibility(fx.ctx, fx.pool, fx.projectID, locationID, "http://127.0.0.1:1/maps"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	after := fx.platformReserved(t)
	if after != before {
		t.Fatalf("platform reserved moved %d -> %d without a budget", before, after)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepMapsVisibility] != StepSkipped {
		t.Fatalf("maps step = %q, want skipped", got.Steps[StepMapsVisibility])
	}
	if got.Status == StatusFailed {
		t.Fatalf("budget skip failed the setup: %q", got.Error)
	}
	var orgReserved int64
	if err := fx.pool.QueryRow(fx.ctx, `SELECT reserved_credits FROM organization_maps_credit_budgets
		WHERE organization_id = $1`, fx.orgID).Scan(&orgReserved); err != nil {
		t.Fatalf("read org budget: %v", err)
	}
	if orgReserved != 0 {
		t.Fatalf("org reserved = %d, want 0", orgReserved)
	}
}

func TestMapsLaunchNeverExceedsDisclosed(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-cap")
	for i, q := range []string{"a in Testville", "b in Testville", "c in Testville"} {
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_location_queries
			(location_id, text, normalized, ordinal, enabled, kind, source, origin)
			VALUES ($1, $2, $3, $4, TRUE, 'map', 'generated', 'service')`,
			locationID, q, q, i); err != nil {
			t.Fatalf("seed query: %v", err)
		}
	}
	tx, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, _, err := StartOrResume(fx.ctx, tx, fx.projectID, locationID, fx.userID, ""); err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("start: %v", err)
	}
	// Disclose less than the 3-query quote (81) to force the clamp.
	if _, err := tx.Exec(fx.ctx, `UPDATE location_setup SET profile_status = 'completed',
		map_queries_status = 'completed', estimated_maps_credits = 27
		WHERE project_id = $1 AND location_id = $2`, fx.projectID, locationID); err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("advance: %v", err)
	}
	if err := tx.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	before := fx.platformReserved(t)
	if err := LaunchMapsVisibility(fx.ctx, fx.pool, fx.projectID, locationID, "http://127.0.0.1:1/maps"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if after := fx.platformReserved(t); after != before {
		t.Fatalf("platform reserved moved %d -> %d past disclosure", before, after)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepMapsVisibility] != StepSkipped {
		t.Fatalf("maps step = %q, want skipped past disclosure", got.Steps[StepMapsVisibility])
	}
	var runs int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM local_visibility_runs WHERE location_id = $1`,
		locationID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runs = %d, want 0 past disclosure", runs)
	}
}

func TestResumePreservesRunningSibling(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-resume")
	tx, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, _, err := StartOrResume(fx.ctx, tx, fx.projectID, locationID, fx.userID, "first"); err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("start: %v", err)
	}
	if err := tx.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := LaunchUnfinished(fx.ctx, fx.pool, fx.projectID, locationID, "http://127.0.0.1:1/maps"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	// Fail only the profile step; questions must stay running (failed setup
	// keeps the in-flight sibling).
	tx2, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	setup, err := getSetupTx(fx.ctx, tx2, fx.projectID, locationID)
	if err != nil {
		_ = tx2.Rollback(fx.ctx)
		t.Fatalf("lock: %v", err)
	}
	if _, err := tx2.Exec(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
		VALUES ('prompt_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`, fx.projectID, locationID); err != nil {
		_ = tx2.Rollback(fx.ctx)
		t.Fatalf("branch job: %v", err)
	}
	if _, err := tx2.Exec(fx.ctx, `UPDATE location_setup SET status = 'failed', error = 'x',
		profile_status = 'failed', questions_status = 'running', completed_at = now()
		WHERE project_id = $1 AND location_id = $2`, fx.projectID, locationID); err != nil {
		_ = tx2.Rollback(fx.ctx)
		t.Fatalf("fail: %v", err)
	}
	_ = setup
	if err := tx2.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	tx3, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	resumed, created, err := StartOrResume(fx.ctx, tx3, fx.projectID, locationID, fx.userID, "second")
	if err != nil {
		_ = tx3.Rollback(fx.ctx)
		t.Fatalf("resume: %v", err)
	}
	if created {
		_ = tx3.Rollback(fx.ctx)
		t.Fatal("resume recreated the row")
	}
	if resumed.Differences != "first" {
		_ = tx3.Rollback(fx.ctx)
		t.Fatalf("resume rewrote differences to %q", resumed.Differences)
	}
	if resumed.Steps[StepQuestions] != StepRunning {
		_ = tx3.Rollback(fx.ctx)
		t.Fatalf("resume reset running sibling to %q", resumed.Steps[StepQuestions])
	}
	if resumed.Steps[StepProfile] != StepPending {
		_ = tx3.Rollback(fx.ctx)
		t.Fatalf("resume left failed step at %q", resumed.Steps[StepProfile])
	}
	if err := tx3.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func (fx setupFixture) startSetup(t *testing.T, locationID pgtype.UUID) {
	t.Helper()
	tx, err := fx.pool.Begin(fx.ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, _, err := StartOrResume(fx.ctx, tx, fx.projectID, locationID, fx.userID, ""); err != nil {
		_ = tx.Rollback(fx.ctx)
		t.Fatalf("start: %v", err)
	}
	if err := tx.Commit(fx.ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func (fx setupFixture) markSteps(t *testing.T, locationID pgtype.UUID, set map[string]string) {
	t.Helper()
	for step, status := range set {
		if _, err := fx.pool.Exec(fx.ctx, `UPDATE location_setup SET `+step+`_status = $3, updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, fx.projectID, locationID, status); err != nil {
			t.Fatalf("mark %s: %v", step, err)
		}
	}
}

func (fx setupFixture) seedQuestions(t *testing.T, locationID pgtype.UUID) {
	t.Helper()
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_ai_questions (project_id, location_id, questions, generation_model)
		VALUES ($1, $2, '["is momo good here"]', 'test')`, fx.projectID, locationID); err != nil {
		t.Fatalf("seed questions: %v", err)
	}
}

func (fx setupFixture) auditUsage(t *testing.T) int {
	t.Helper()
	var used int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COALESCE(SUM(used_audits), 0) FROM ai_workspace_monthly_usage
		WHERE organization_id = $1`, fx.orgID).Scan(&used); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return used
}

func TestEmptyQuestionsConsumeNoQuota(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-noq")
	fx.startSetup(t, locationID)
	fx.markSteps(t, locationID, map[string]string{StepProfile: StepCompleted, StepQuestions: StepCompleted})
	if err := LaunchAIVisibility(fx.ctx, fx.pool, fx.projectID, locationID); err != nil {
		t.Fatalf("launch: %v", err)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepQuestions] != StepFailed || got.Status != StatusFailed {
		t.Fatalf("empty questions did not fail visibly: steps=%v status=%q", got.Steps, got.Status)
	}
	if used := fx.auditUsage(t); used != 0 {
		t.Fatalf("quota consumed with no questions: used=%d", used)
	}
}

func TestAIQuotaSkipSpendsNothing(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-quota")
	fx.startSetup(t, locationID)
	fx.markSteps(t, locationID, map[string]string{StepProfile: StepCompleted, StepQuestions: StepCompleted})
	fx.seedQuestions(t, locationID)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_workspace_monthly_usage (organization_id, period_start, used_audits)
		VALUES ($1, (date_trunc('month', now() AT TIME ZONE 'UTC'))::date, 10)`, fx.orgID); err != nil {
		t.Fatalf("exhaust quota: %v", err)
	}
	if err := LaunchAIVisibility(fx.ctx, fx.pool, fx.projectID, locationID); err != nil {
		t.Fatalf("launch: %v", err)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepAIVisibility] != StepSkipped || got.Status == StatusFailed {
		t.Fatalf("quota exhaustion did not skip cleanly: steps=%v status=%q", got.Steps, got.Status)
	}
	if !got.AIAuditID.Valid {
		// No audit launched: ai_audit_id must stay null.
	} else {
		t.Fatalf("audit launched despite exhausted quota: %s", got.AIAuditID.String())
	}
	if used := fx.auditUsage(t); used != 10 {
		t.Fatalf("quota moved on skip: used=%d", used)
	}
}

func TestAISuccessLaunchesAudit(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-aiok")
	fx.startSetup(t, locationID)
	fx.markSteps(t, locationID, map[string]string{StepProfile: StepCompleted, StepQuestions: StepCompleted})
	fx.seedQuestions(t, locationID)
	if err := LaunchAIVisibility(fx.ctx, fx.pool, fx.projectID, locationID); err != nil {
		t.Fatalf("launch: %v", err)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepAIVisibility] != StepRunning || !got.AIAuditID.Valid {
		t.Fatalf("audit not launched: steps=%v", got.Steps)
	}
	var auditStatus string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT status FROM ai_audits WHERE id = $1`, got.AIAuditID).Scan(&auditStatus); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if auditStatus != "queued" {
		t.Fatalf("audit status = %q, want queued", auditStatus)
	}
	var jobs int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM ai_worker_jobs
		WHERE audit_id = $1 AND status = 'pending'`, got.AIAuditID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("visibility jobs = %d, err = %v, want 1 pending", jobs, err)
	}
	if used := fx.auditUsage(t); used != 1 {
		t.Fatalf("quota used = %d, want 1", used)
	}
}

func TestMapsSuccessReservesAndSettles(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-mapsok")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO project_location_queries
		(location_id, text, normalized, ordinal, enabled, kind, source, origin)
		VALUES ($1, 'momo in Testville', 'momo in testville', 0, TRUE, 'map', 'generated', 'service')`, locationID); err != nil {
		t.Fatalf("seed query: %v", err)
	}
	fx.startSetup(t, locationID)
	fx.markSteps(t, locationID, map[string]string{StepProfile: StepCompleted, StepMapQueries: StepCompleted})
	before := fx.platformReserved(t)
	if err := LaunchMapsVisibility(fx.ctx, fx.pool, fx.projectID, locationID, "http://127.0.0.1:1/maps"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepMapsVisibility] != StepRunning || !got.MapsRunID.Valid {
		t.Fatalf("run not launched: steps=%v", got.Steps)
	}
	if got.EstimatedMapsCredits == nil || *got.EstimatedMapsCredits != 27 {
		t.Fatalf("estimated = %v, want 27", got.EstimatedMapsCredits)
	}
	if after := fx.platformReserved(t); after-before != 27 {
		t.Fatalf("platform reserved delta = %d, want 27", after-before)
	}
	// Release the shared singleton reservation and remove the run.
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE platform_maps_credit_budget
		SET reserved_credits = reserved_credits - 27 WHERE id = TRUE`); err != nil {
		t.Fatalf("release platform: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE organization_maps_credit_budgets
		SET reserved_credits = reserved_credits - 27 WHERE organization_id = $1`, fx.orgID); err != nil {
		t.Fatalf("release org: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `DELETE FROM local_visibility_runs WHERE id = $1`, got.MapsRunID); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if after := fx.platformReserved(t); after != before {
		t.Fatalf("platform reserved not restored: %d vs %d", after, before)
	}
}

func TestParallelSiblingFinalizers(t *testing.T) {
	fx := newSetupFixture(t)
	locationID := fx.newLocation(t, "place-siblings")
	fx.startSetup(t, locationID)
	fx.markSteps(t, locationID, map[string]string{
		StepProfile: StepCompleted, StepQuestions: StepRunning, StepMapQueries: StepRunning,
	})
	fx.seedQuestions(t, locationID)
	var qJob, mJob pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
		VALUES ('prompt_generation', $1, $2, 'running') RETURNING id`,
		fx.projectID, locationID).Scan(&qJob); err != nil {
		t.Fatalf("question job: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
		VALUES ('map_query_generation', $1, $2, 'running') RETURNING id`,
		fx.projectID, locationID).Scan(&mJob); err != nil {
		t.Fatalf("map job: %v", err)
	}
	if err := FinalizeQuestionsSuccess(fx.ctx, fx.pool, fx.projectID, locationID, qJob); err != nil {
		t.Fatalf("finalize questions: %v", err)
	}
	if err := FinalizeMapQueriesFailure(fx.ctx, fx.pool, fx.projectID, locationID, mJob, "provider blew up"); err != nil {
		t.Fatalf("finalize maps: %v", err)
	}
	got, err := Get(fx.ctx, fx.pool, fx.projectID, locationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Steps[StepQuestions] != StepCompleted {
		t.Fatalf("questions = %q, want completed", got.Steps[StepQuestions])
	}
	if got.Steps[StepAIVisibility] != StepRunning || !got.AIAuditID.Valid {
		t.Fatalf("ai branch did not launch: steps=%v", got.Steps)
	}
	if got.Steps[StepMapQueries] != StepFailed || got.Status != StatusFailed {
		t.Fatalf("map failure not recorded: steps=%v status=%q", got.Steps, got.Status)
	}
}
