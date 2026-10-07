package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	locAuditITDatabaseEnv    = "LOCATION_AI_AUDIT_TEST_DATABASE_URL"
	locAuditITDatabasePrefix = "revserp_layer_c_"
)

func locAuditITPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	raw := os.Getenv(locAuditITDatabaseEnv)
	if raw == "" {
		t.Skip(locAuditITDatabaseEnv + " is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", locAuditITDatabaseEnv, err)
	}
	if name := strings.Trim(parsed.Path, "/"); !strings.HasPrefix(name, locAuditITDatabasePrefix) {
		t.Fatalf("refusing %s database %q: want a %s* scratch database", locAuditITDatabaseEnv, name, locAuditITDatabasePrefix)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("refusing %s host %q: want 127.0.0.1 or localhost", locAuditITDatabaseEnv, host)
	}
	if parsed.Port() != "5432" {
		t.Fatalf("refusing %s port %q: want 5432", locAuditITDatabaseEnv, parsed.Port())
	}
	ctx := context.Background()
	pool, err := Connect(ctx, raw, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("%s is not available: %v", locAuditITDatabaseEnv, err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

type locAuditITFixture struct {
	t   *testing.T
	ctx context.Context
	tx  pgx.Tx
	q   *sqlc.Queries

	orgID  pgtype.UUID
	projA  pgtype.UUID
	projB  pgtype.UUID
	crawlA pgtype.UUID
	crawlB pgtype.UUID
	locA   pgtype.UUID
	locA2  pgtype.UUID
	locB   pgtype.UUID
}

func newLocAuditITFixture(t *testing.T, pool *pgxpool.Pool, ctx context.Context) *locAuditITFixture {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin location audit fixture: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	fx := &locAuditITFixture{t: t, ctx: ctx, tx: tx, q: sqlc.New(tx)}
	prefix := fmt.Sprintf("locait-%d", time.Now().UnixNano())

	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, prefix).Scan(&fx.orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	newProject := func() pgtype.UUID {
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,$2,'https://locait.example') RETURNING id`,
			fx.orgID, prefix).Scan(&id); err != nil {
			t.Fatalf("create project: %v", err)
		}
		return id
	}
	fx.projA, fx.projB = newProject(), newProject()

	newCrawl := func(project pgtype.UUID) pgtype.UUID {
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, project).Scan(&id); err != nil {
			t.Fatalf("create crawl: %v", err)
		}
		return id
	}
	fx.crawlA, fx.crawlB = newCrawl(fx.projA), newCrawl(fx.projA)

	newLocation := func(project pgtype.UUID, name string) pgtype.UUID {
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,$2,39.7392,-104.9903) RETURNING id`,
			project, name).Scan(&id); err != nil {
			t.Fatalf("create location %q: %v", name, err)
		}
		return id
	}
	fx.locA = newLocation(fx.projA, prefix+"-loc-a")
	fx.locA2 = newLocation(fx.projA, prefix+"-loc-a2")
	fx.locB = newLocation(fx.projB, prefix+"-loc-b")
	return fx
}

func (fx *locAuditITFixture) insertAudit(project, location, crawl pgtype.UUID, status string) pgtype.UUID {
	fx.t.Helper()
	var id pgtype.UUID
	if err := fx.tx.QueryRow(fx.ctx,
		`INSERT INTO ai_audits (project_id, location_id, crawl_id, status) VALUES ($1,$2,$3,$4) RETURNING id`,
		project, location, crawl, status).Scan(&id); err != nil {
		fx.t.Fatalf("insert ai_audit status=%s: %v", status, err)
	}
	return id
}

func (fx *locAuditITFixture) scanAuditStatus(id pgtype.UUID) (string, pgtype.Text) {
	fx.t.Helper()
	var status string
	var message pgtype.Text
	if err := fx.tx.QueryRow(fx.ctx, `SELECT status, error_message FROM ai_audits WHERE id=$1`, id).Scan(&status, &message); err != nil {
		fx.t.Fatalf("scan audit status: %v", err)
	}
	return status, message
}

func (fx *locAuditITFixture) scanJobStatus(id pgtype.UUID) (string, pgtype.Text) {
	fx.t.Helper()
	var status string
	var message pgtype.Text
	if err := fx.tx.QueryRow(fx.ctx, `SELECT status, error_message FROM ai_worker_jobs WHERE id=$1`, id).Scan(&status, &message); err != nil {
		fx.t.Fatalf("scan job status: %v", err)
	}
	return status, message
}

func (fx *locAuditITFixture) expectSQLError(wantCode, wantConstraint, sql string, args ...any) {
	fx.t.Helper()
	if _, err := fx.tx.Exec(fx.ctx, `SAVEPOINT locait_error_probe`); err != nil {
		fx.t.Fatalf("create error probe savepoint: %v", err)
	}
	_, err := fx.tx.Exec(fx.ctx, sql, args...)
	if err == nil {
		fx.t.Fatalf("expected SQL error %s but statement succeeded: %s", wantCode, sql)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		fx.t.Fatalf("expected *pgconn.PgError %s, got %T: %v", wantCode, err, err)
	}
	if pgErr.Code != wantCode {
		fx.t.Errorf("SQL error code = %s, want %s (%v)", pgErr.Code, wantCode, err)
	}
	if wantConstraint != "" && pgErr.ConstraintName != wantConstraint {
		fx.t.Errorf("SQL constraint = %q, want %q", pgErr.ConstraintName, wantConstraint)
	}
	if _, err := fx.tx.Exec(fx.ctx, `ROLLBACK TO SAVEPOINT locait_error_probe`); err != nil {
		fx.t.Fatalf("rollback to error probe savepoint: %v", err)
	}
	if _, err := fx.tx.Exec(fx.ctx, `RELEASE SAVEPOINT locait_error_probe`); err != nil {
		fx.t.Fatalf("release error probe savepoint: %v", err)
	}
}

func (fx *locAuditITFixture) insertSetup(status string) {
	fx.t.Helper()
	if _, err := fx.tx.Exec(fx.ctx, `INSERT INTO project_setup (organization_id, project_id, status) VALUES ($1,$2,$3)`,
		fx.orgID, fx.projA, status); err != nil {
		fx.t.Fatalf("insert project_setup: %v", err)
	}
}

func (fx *locAuditITFixture) insertStaleVisibilityJob(auditID pgtype.UUID) pgtype.UUID {
	fx.t.Helper()
	var id pgtype.UUID
	if err := fx.tx.QueryRow(fx.ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, audit_id, status, started_at)
		VALUES ('visibility_run', $1, $2, 'running', now() - interval '1 hour') RETURNING id`, fx.projA, auditID).Scan(&id); err != nil {
		fx.t.Fatalf("insert stale visibility job: %v", err)
	}
	return id
}

func (fx *locAuditITFixture) scanSetup() (string, pgtype.Text) {
	fx.t.Helper()
	var status string
	var failedStep pgtype.Text
	if err := fx.tx.QueryRow(fx.ctx, `SELECT status, failed_step FROM project_setup WHERE project_id=$1`, fx.projA).Scan(&status, &failedStep); err != nil {
		fx.t.Fatalf("scan project_setup: %v", err)
	}
	return status, failedStep
}

func TestLocationAIAuditIntegration(t *testing.T) {
	pool, ctx := locAuditITPool(t)

	t.Run("composite FK rejects cross-project location", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		fx.expectSQLError("23503", "ai_audits_location_id_project_id_fkey",
			`INSERT INTO ai_audits (project_id, location_id, status) VALUES ($1,$2,'queued')`, fx.projA, fx.locB)

		good := fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "queued")
		var locationID pgtype.UUID
		if err := fx.tx.QueryRow(ctx, `SELECT location_id FROM ai_audits WHERE id=$1`, good).Scan(&locationID); err != nil {
			t.Fatal(err)
		}
		if !locationID.Valid || locationID != fx.locA {
			t.Fatalf("matching location audit location_id = %v, want %v", locationID, fx.locA)
		}
	})

	t.Run("NULL project audit stays legacy", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		legacy := fx.insertAudit(fx.projA, pgtype.UUID{}, fx.crawlA, "queued")
		audit, err := fx.q.GetAIAuditForWorker(ctx, sqlc.GetAIAuditForWorkerParams{ID: legacy, ProjectID: fx.projA})
		if err != nil {
			t.Fatalf("load legacy audit: %v", err)
		}
		if audit.LocationID.Valid {
			t.Fatalf("legacy project audit unexpectedly has location_id %s", audit.LocationID.String())
		}
		if !audit.CrawlID.Valid || audit.CrawlID != fx.crawlA {
			t.Fatalf("legacy project audit crawl_id = %v, want %v", audit.CrawlID, fx.crawlA)
		}
	})

	t.Run("active project and location guards are independent", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		projectAudit := fx.insertAudit(fx.projA, pgtype.UUID{}, fx.crawlA, "queued")
		locationAudit := fx.insertAudit(fx.projA, fx.locA, fx.crawlA, "queued")

		byCrawl, err := fx.q.GetActiveAIAuditByCrawlAndProject(ctx, sqlc.GetActiveAIAuditByCrawlAndProjectParams{ProjectID: fx.projA, CrawlID: fx.crawlA})
		if err != nil {
			t.Fatalf("active project guard: %v", err)
		}
		if byCrawl.ID != projectAudit || byCrawl.LocationID.Valid {
			t.Fatalf("project crawl guard returned %s (location %v)", byCrawl.ID.String(), byCrawl.LocationID.Valid)
		}

		byLocation, err := fx.q.GetActiveAIAuditByLocationAndProject(ctx, sqlc.GetActiveAIAuditByLocationAndProjectParams{ProjectID: fx.projA, LocationID: fx.locA})
		if err != nil {
			t.Fatalf("active location guard: %v", err)
		}
		if byLocation.ID != locationAudit || !byLocation.LocationID.Valid || byLocation.LocationID != fx.locA {
			t.Fatalf("location guard returned %s (location %v)", byLocation.ID.String(), byLocation.LocationID.Valid)
		}

		if _, err := fx.q.GetActiveAIAuditByCrawlAndProject(ctx, sqlc.GetActiveAIAuditByCrawlAndProjectParams{ProjectID: fx.projA, CrawlID: fx.crawlB}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("location audit leaked into a project crawl guard: %v", err)
		}
		if _, err := fx.q.GetActiveAIAuditByLocationAndProject(ctx, sqlc.GetActiveAIAuditByLocationAndProjectParams{ProjectID: fx.projA, LocationID: fx.locA2}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("project audit leaked into a location guard: %v", err)
		}
	})

	t.Run("duplicate location guard ignores crawl but permits other locations", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		fx.insertAudit(fx.projA, fx.locA, fx.crawlA, "queued")
		fx.expectSQLError("23505", "idx_ai_audits_one_active_per_location",
			`INSERT INTO ai_audits (project_id, location_id, crawl_id, status) VALUES ($1,$2,$3,'queued')`, fx.projA, fx.locA, fx.crawlB)
		fx.expectSQLError("23505", "idx_ai_audits_one_active_per_location",
			`INSERT INTO ai_audits (project_id, location_id, status) VALUES ($1,$2,'running')`, fx.projA, fx.locA)
		fx.insertAudit(fx.projA, fx.locA2, pgtype.UUID{}, "queued")
	})

	t.Run("terminal location rerun is allowed", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		fx.insertAudit(fx.projA, fx.locA, fx.crawlA, "completed")
		fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "failed")
		fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "queued")
		fx.expectSQLError("23505", "idx_ai_audits_one_active_per_location",
			`INSERT INTO ai_audits (project_id, location_id, status) VALUES ($1,$2,'running')`, fx.projA, fx.locA)

		history, err := fx.q.ListAIAuditsForLocation(ctx, sqlc.ListAIAuditsForLocationParams{
			ProjectID: fx.projA, LocationID: fx.locA, StatusFilter: "", PageOffset: 0, PageLimit: 50,
		})
		if err != nil {
			t.Fatalf("list location history: %v", err)
		}
		if len(history) != 3 {
			t.Fatalf("location history length = %d, want 3 terminal+active rows", len(history))
		}
	})

	t.Run("project-scoped queries ignore location audits", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		projectAudit := fx.insertAudit(fx.projA, pgtype.UUID{}, fx.crawlA, "completed")
		locationAudit := fx.insertAudit(fx.projA, fx.locA, fx.crawlA, "completed")
		otherLocationAudit := fx.insertAudit(fx.projA, fx.locA2, pgtype.UUID{}, "completed")
		activeProject := fx.insertAudit(fx.projA, pgtype.UUID{}, fx.crawlB, "queued")
		activeLocation := fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "running")

		count, err := fx.q.CountAIAuditsForProject(ctx, sqlc.CountAIAuditsForProjectParams{ProjectID: fx.projA, Column2: ""})
		if err != nil {
			t.Fatalf("count project audits: %v", err)
		}
		if count != 2 {
			t.Errorf("project audit count = %d, want 2 (location rows excluded)", count)
		}

		list, err := fx.q.ListAIAuditsForProject(ctx, sqlc.ListAIAuditsForProjectParams{ProjectID: fx.projA, Column2: "", Limit: 50, Offset: 0})
		if err != nil {
			t.Fatalf("list project audits: %v", err)
		}
		ids := map[pgtype.UUID]bool{}
		for _, audit := range list {
			if audit.LocationID.Valid {
				t.Errorf("project list leaked location audit %s", audit.ID.String())
			}
			ids[audit.ID] = true
		}
		if len(list) != 2 || !ids[projectAudit] || !ids[activeProject] {
			t.Fatalf("project list = %v, want projectAudit+activeProject only", ids)
		}

		byCrawl, err := fx.q.GetAIAuditByCrawlAndProject(ctx, sqlc.GetAIAuditByCrawlAndProjectParams{ProjectID: fx.projA, CrawlID: fx.crawlA})
		if err != nil {
			t.Fatalf("get audit by crawl: %v", err)
		}
		if byCrawl.ID != projectAudit {
			t.Fatalf("crawl lookup returned %s, want project audit %s", byCrawl.ID.String(), projectAudit.String())
		}

		affected, err := fx.q.FailActiveAIAuditsForCrawl(ctx, sqlc.FailActiveAIAuditsForCrawlParams{ProjectID: fx.projA, CrawlID: fx.crawlB})
		if err != nil {
			t.Fatalf("fail active audits: %v", err)
		}
		if affected != 1 {
			t.Errorf("fail-active affected %d rows, want 1 project row", affected)
		}
		if status, _ := fx.scanAuditStatus(activeProject); status != "failed" {
			t.Errorf("active project audit status = %s, want failed", status)
		}
		locationStatus, locationErr := fx.scanAuditStatus(activeLocation)
		if locationStatus != "running" || locationErr.Valid {
			t.Errorf("location audit changed: status=%s error=%v", locationStatus, locationErr)
		}

		locationList, err := fx.q.ListAIAuditsForLocation(ctx, sqlc.ListAIAuditsForLocationParams{
			ProjectID: fx.projA, LocationID: fx.locA, StatusFilter: "", PageOffset: 0, PageLimit: 50,
		})
		if err != nil {
			t.Fatalf("list location audits: %v", err)
		}
		locationIDs := map[pgtype.UUID]bool{}
		for _, audit := range locationList {
			if audit.LocationID != fx.locA {
				t.Errorf("location list returned audit %s for location %s", audit.ID.String(), audit.LocationID.String())
			}
			locationIDs[audit.ID] = true
		}
		if len(locationList) != 2 || !locationIDs[locationAudit] || !locationIDs[activeLocation] {
			t.Fatalf("location list = %v, want locationAudit+activeLocation only", locationIDs)
		}
		if locationIDs[otherLocationAudit] {
			t.Errorf("location list leaked locA2 audit %s", otherLocationAudit.String())
		}
	})

	t.Run("worker reads only the selected location enabled map queries in order", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		insertQuery := func(location pgtype.UUID, text string, ordinal int32, enabled bool, kind string) {
			if _, err := fx.tx.Exec(ctx, `INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin)
				VALUES ($1,$2,$3,$4,$5,$6,'manual','service')`,
				location, text, strings.ToLower(text), ordinal, enabled, kind); err != nil {
				t.Fatalf("insert location query %q: %v", text, err)
			}
		}
		insertQuery(fx.locA, "map zero", 0, true, "map")
		insertQuery(fx.locA, "map one", 1, true, "map")
		insertQuery(fx.locA, "disabled map", 2, false, "map")
		insertQuery(fx.locA, "ai question", 3, true, "ai_question")
		insertQuery(fx.locA, "map four", 4, true, "map")
		insertQuery(fx.locA2, "other location map", 0, true, "map")

		got, err := fx.q.ListEnabledMapQueriesForLocation(ctx, sqlc.ListEnabledMapQueriesForLocationParams{LocationID: fx.locA, ProjectID: fx.projA})
		if err != nil {
			t.Fatalf("list enabled map queries: %v", err)
		}
		texts := make([]string, 0, len(got))
		for _, query := range got {
			if query.LocationID != fx.locA {
				t.Errorf("query %q belongs to location %s", query.Text, query.LocationID.String())
			}
			texts = append(texts, query.Text)
		}
		want := []string{"map zero", "map one", "map four"}
		if !slices.Equal(texts, want) {
			t.Fatalf("enabled map queries = %v, want %v", texts, want)
		}

		cross, err := fx.q.ListEnabledMapQueriesForLocation(ctx, sqlc.ListEnabledMapQueriesForLocationParams{LocationID: fx.locA, ProjectID: fx.projB})
		if err != nil {
			t.Fatalf("cross-project map query read: %v", err)
		}
		if len(cross) != 0 {
			t.Fatalf("cross-project read returned %d rows, want 0", len(cross))
		}

		if _, err := fx.q.GetLocationForAIAuditWorker(ctx, sqlc.GetLocationForAIAuditWorkerParams{LocationID: fx.locA, ProjectID: fx.projA}); err != nil {
			t.Fatalf("load matching worker location: %v", err)
		}
		if _, err := fx.q.GetLocationForAIAuditWorker(ctx, sqlc.GetLocationForAIAuditWorkerParams{LocationID: fx.locA, ProjectID: fx.projB}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("worker location lookup crossed projects: %v", err)
		}
	})

	t.Run("stale location visibility job fails without failing setup", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		locationAudit := fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "running")
		fx.insertSetup("visibility")
		jobID := fx.insertStaleVisibilityJob(locationAudit)

		if err := fx.q.ReclaimStaleRunningAIWorkerJobs(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true}); err != nil {
			t.Fatalf("reclaim stale jobs: %v", err)
		}

		var jobStatus string
		var jobErr pgtype.Text
		if err := fx.tx.QueryRow(ctx, `SELECT status, error_message FROM ai_worker_jobs WHERE id=$1`, jobID).Scan(&jobStatus, &jobErr); err != nil {
			t.Fatalf("scan reclaimed job: %v", err)
		}
		if jobStatus != "failed" {
			t.Errorf("job status = %s, want failed", jobStatus)
		}
		if setupStatus, failedStep := fx.scanSetup(); setupStatus != "visibility" || failedStep.Valid {
			t.Errorf("project setup = %s/%v, want visibility with no failed_step", setupStatus, failedStep)
		}
	})

	t.Run("stale project visibility job fails setup", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		projectAudit := fx.insertAudit(fx.projA, pgtype.UUID{}, fx.crawlA, "running")
		fx.insertSetup("visibility")
		fx.insertStaleVisibilityJob(projectAudit)

		if err := fx.q.ReclaimStaleRunningAIWorkerJobs(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true}); err != nil {
			t.Fatalf("reclaim stale jobs: %v", err)
		}
		setupStatus, failedStep := fx.scanSetup()
		if setupStatus != "failed" || !failedStep.Valid || failedStep.String != "visibility" {
			t.Errorf("project setup = %s/%v, want failed/visibility", setupStatus, failedStep)
		}
	})

	t.Run("crash-before-running location audit is reclaimed and unblocks reruns", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		crashAudit := fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "queued")
		fx.insertSetup("visibility")
		jobID := fx.insertStaleVisibilityJob(crashAudit)

		fx.expectSQLError("23505", "idx_ai_audits_one_active_per_location",
			`INSERT INTO ai_audits (project_id, location_id, status) VALUES ($1,$2,'queued')`, fx.projA, fx.locA)

		if err := fx.q.ReclaimStaleRunningAIWorkerJobs(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true}); err != nil {
			t.Fatalf("reclaim stale jobs: %v", err)
		}

		if status, message := fx.scanAuditStatus(crashAudit); status != "failed" || !message.Valid {
			t.Errorf("crashed location audit = %s (error %v), want failed with a reclaim reason", status, message)
		}
		if jobStatus, _ := fx.scanJobStatus(jobID); jobStatus != "failed" {
			t.Errorf("job status = %s, want failed", jobStatus)
		}
		if setupStatus, failedStep := fx.scanSetup(); setupStatus != "visibility" || failedStep.Valid {
			t.Errorf("project setup = %s/%v, want visibility with no failed_step", setupStatus, failedStep)
		}

		fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "queued")
	})

	t.Run("stale running location audit is reclaimed to failed", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		runningAudit := fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "running")
		fx.insertSetup("visibility")
		jobID := fx.insertStaleVisibilityJob(runningAudit)

		if err := fx.q.ReclaimStaleRunningAIWorkerJobs(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true}); err != nil {
			t.Fatalf("reclaim stale jobs: %v", err)
		}
		if status, message := fx.scanAuditStatus(runningAudit); status != "failed" || !message.Valid {
			t.Errorf("running location audit = %s (error %v), want failed", status, message)
		}
		if jobStatus, _ := fx.scanJobStatus(jobID); jobStatus != "failed" {
			t.Errorf("job status = %s, want failed", jobStatus)
		}
		if setupStatus, failedStep := fx.scanSetup(); setupStatus != "visibility" || failedStep.Valid {
			t.Errorf("project setup = %s/%v, want visibility with no failed_step", setupStatus, failedStep)
		}
		fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "queued")
	})

	t.Run("project reclaim behavior is unchanged", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		projectAudit := fx.insertAudit(fx.projA, pgtype.UUID{}, fx.crawlA, "queued")
		fx.insertSetup("visibility")
		jobID := fx.insertStaleVisibilityJob(projectAudit)

		if err := fx.q.ReclaimStaleRunningAIWorkerJobs(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true}); err != nil {
			t.Fatalf("reclaim stale jobs: %v", err)
		}
		if jobStatus, _ := fx.scanJobStatus(jobID); jobStatus != "failed" {
			t.Errorf("job status = %s, want failed", jobStatus)
		}
		if status, _ := fx.scanAuditStatus(projectAudit); status != "queued" {
			t.Errorf("project audit status = %s, want queued (unchanged)", status)
		}
		if setupStatus, failedStep := fx.scanSetup(); setupStatus != "failed" || !failedStep.Valid || failedStep.String != "visibility" {
			t.Errorf("project setup = %s/%v, want failed/visibility", setupStatus, failedStep)
		}
	})

	t.Run("mentioned_branch stays unknown for legacy and failed runs", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		audit := fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "completed")

		legacyRun, err := fx.q.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
			AuditID: audit, QuestionText: "legacy", DisplayOrder: 1, ModelName: "test-model", Status: "failed",
		})
		if err != nil {
			t.Fatalf("insert legacy run: %v", err)
		}
		if legacyRun.MentionedBranch.Valid {
			t.Errorf("legacy run mentioned_branch = %v, want NULL", legacyRun.MentionedBranch)
		}

		completedRun, err := fx.q.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
			AuditID: audit, QuestionText: "located", DisplayOrder: 2, ModelName: "test-model", Status: "success",
			MentionedBranch: pgtype.Bool{Bool: true, Valid: true},
		})
		if err != nil {
			t.Fatalf("insert completed run: %v", err)
		}
		if !completedRun.MentionedBranch.Valid || !completedRun.MentionedBranch.Bool {
			t.Errorf("completed run mentioned_branch = %v, want true", completedRun.MentionedBranch)
		}

		runs, err := fx.q.ListAIAuditRunsByAuditID(ctx, audit)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) != 2 {
			t.Fatalf("run count = %d, want 2", len(runs))
		}
		for _, run := range runs {
			if run.DisplayOrder == 1 && run.MentionedBranch.Valid {
				t.Errorf("legacy run loaded with mentioned_branch %v", run.MentionedBranch)
			}
		}
	})

	t.Run("location FK is NO ACTION and preserves audit history", func(t *testing.T) {
		fx := newLocAuditITFixture(t, pool, ctx)
		var deleteAction byte
		if err := fx.tx.QueryRow(ctx, `SELECT confdeltype FROM pg_constraint WHERE conname='ai_audits_location_id_project_id_fkey' AND conrelid='ai_audits'::regclass`).Scan(&deleteAction); err != nil {
			t.Fatalf("read composite FK delete action: %v", err)
		}
		if deleteAction != 'a' {
			t.Errorf("composite FK confdeltype = %q, want 'a' (NO ACTION)", deleteAction)
		}

		fx.insertAudit(fx.projA, fx.locA, pgtype.UUID{}, "completed")
		fx.expectSQLError("23503", "ai_audits_location_id_project_id_fkey",
			`DELETE FROM project_locations WHERE id=$1`, fx.locA)
	})
}
