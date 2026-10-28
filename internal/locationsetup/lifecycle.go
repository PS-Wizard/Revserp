package locationsetup

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FinalizeProfileSuccess completes the shared profile step and fans out to
// the questions and map-query branches. Duplicates complete the job only.
func FinalizeProfileSuccess(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := markJobCompleted(ctx, tx, jobID); err != nil {
		return err
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[StepProfile] != StepRunning {
		return tx.Commit(ctx)
	}
	if err := completeStep(ctx, tx, setup, StepProfile); err != nil {
		return err
	}
	if setup.Steps[StepKeywordAnalysis] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('location_keyword_analysis', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			projectID, locationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET keyword_analysis_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, projectID, locationID); err != nil {
			return err
		}
	}
	if setup.Steps[StepQuestions] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('prompt_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			projectID, locationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET questions_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, projectID, locationID); err != nil {
			return err
		}
	}
	if setup.Steps[StepMapQueries] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('map_query_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			projectID, locationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET map_queries_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, projectID, locationID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// FinalizeKeywordAnalysisSuccess completes the keyword-estimates step. The
// questions and map-queries branches advance independently of it.
func FinalizeKeywordAnalysisSuccess(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := markJobCompleted(ctx, tx, jobID); err != nil {
		return err
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[StepKeywordAnalysis] != StepRunning {
		return tx.Commit(ctx)
	}
	if err := completeStep(ctx, tx, setup, StepKeywordAnalysis); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinalizeKeywordAnalysisFailure fails the keyword-estimates step. Other
// branches still finish on their own; the POST resume relaunches only
// unfinished work.
func FinalizeKeywordAnalysisFailure(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID, message string) error {
	return failOwnedJob(ctx, pool, projectID, locationID, jobID, StepKeywordAnalysis, message)
}

// FinalizeKeywordAnalysisEmpty skips the step when the location has no saved
// keywords yet. Estimates need input phrases, and an empty location is
// normal (keywords arrive later via the UI or RevBot), never a setup
// failure; the RevBot estimate tool backfills them on demand.
func FinalizeKeywordAnalysisEmpty(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := markJobCompleted(ctx, tx, jobID); err != nil {
		return err
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[StepKeywordAnalysis] != StepRunning {
		return tx.Commit(ctx)
	}
	if err := skipStep(ctx, tx, setup, StepKeywordAnalysis, SkipKeywordAnalysisNoKeywords); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinalizeProfileFailure fails the profile step. Other branches still finish
// on their own; the POST resume relaunches only unfinished work.
func FinalizeProfileFailure(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID, message string) error {
	return failOwnedJob(ctx, pool, projectID, locationID, jobID, StepProfile, message)
}

func failOwnedJob(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID, step, message string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := markJobFailed(ctx, tx, jobID, message); err != nil {
		return err
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[step] != StepRunning {
		return tx.Commit(ctx)
	}
	if err := failStep(ctx, tx, setup, step, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LaunchUnfinished enqueues every launchable setup-owned job and runs the
// quota/credit launches. Steps already running, completed, or skipped are
// left alone and job inserts are ON CONFLICT DO NOTHING, so repeats never
// duplicate paid work.
func LaunchUnfinished(ctx context.Context, pool *pgxpool.Pool, projectID, locationID pgtype.UUID, mapsEndpoint string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err := launchUnfinishedTx(ctx, tx, setup, mapsEndpoint); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func launchUnfinishedTx(ctx context.Context, tx pgx.Tx, setup Setup, mapsEndpoint string) error {
	if setup.Status != StatusRunning {
		return nil
	}
	if setup.Steps[StepPrepare] == StepPending {
		if err := completeStep(ctx, tx, setup, StepPrepare); err != nil {
			return err
		}
		setup.Steps[StepPrepare] = StepCompleted
	}
	profile, questions, mapQueries, aiAudit, mapsRun := Launchable(setup.Steps)
	if profile && setup.Steps[StepProfile] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('location_profile_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET profile_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		setup.Steps[StepProfile] = StepRunning
	}
	if setup.Steps[StepProfile] == StepCompleted && setup.Steps[StepKeywordAnalysis] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('location_keyword_analysis', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET keyword_analysis_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		setup.Steps[StepKeywordAnalysis] = StepRunning
	}
	if questions && setup.Steps[StepQuestions] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('prompt_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET questions_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		setup.Steps[StepQuestions] = StepRunning
	}
	if mapQueries && setup.Steps[StepMapQueries] == StepPending {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
			VALUES ('map_query_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
			setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET map_queries_status = 'running', updated_at = now()
			WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID); err != nil {
			return err
		}
		setup.Steps[StepMapQueries] = StepRunning
	}
	if aiAudit {
		if err := launchAIVisibilityTx(ctx, tx, setup); err != nil {
			return err
		}
	}
	if mapsRun {
		if err := launchMapsVisibilityTx(ctx, tx, setup, mapsEndpoint); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileRunningSetups drives pending steps on running setups: the healing
// path for a crash between the POST commit and its launches, or a deferred
// launch whose blocker cleared. Failed setups wait for an explicit POST.
func ReconcileRunningSetups(ctx context.Context, pool *pgxpool.Pool, mapsEndpoint string) error {
	rows, err := pool.Query(ctx, `SELECT project_id, location_id FROM location_setup
		WHERE status = 'running' AND 'pending' IN (prepare_status, profile_status,
			keyword_analysis_status, questions_status, map_queries_status, ai_visibility_status, maps_visibility_status)
		ORDER BY updated_at LIMIT 50`)
	if err != nil {
		return err
	}
	var targets [][2]pgtype.UUID
	for rows.Next() {
		var projectID, locationID pgtype.UUID
		if err := rows.Scan(&projectID, &locationID); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, [2]pgtype.UUID{projectID, locationID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, target := range targets {
		if err := LaunchUnfinished(ctx, pool, target[0], target[1], mapsEndpoint); err != nil {
			return err
		}
	}
	return nil
}

// ReclaimSweep fails running steps whose latest owning job already failed,
// whether the setup itself is running or already failed, so one crashed
// branch never disables recovery of its siblings.
func ReclaimSweep(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT project_id, location_id FROM location_setup WHERE status IN ('running', 'failed')`)
	if err != nil {
		return err
	}
	var targets [][2]pgtype.UUID
	for rows.Next() {
		var projectID, locationID pgtype.UUID
		if err := rows.Scan(&projectID, &locationID); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, [2]pgtype.UUID{projectID, locationID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, target := range targets {
		if err := reclaimSetup(ctx, pool, target[0], target[1]); err != nil {
			return err
		}
	}
	return nil
}

func reclaimSetup(ctx context.Context, pool *pgxpool.Pool, projectID, locationID pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	steps := []struct {
		step    string
		jobType string
		scoped  string
	}{
		{StepProfile, JobLocationProfileGeneration, ""},
		{StepKeywordAnalysis, JobLocationKeywordAnalysis, ""},
		{StepQuestions, JobPromptGeneration, ""},
		{StepMapQueries, JobMapQueryGeneration, ""},
	}
	for _, s := range steps {
		if setup.Steps[s.step] != StepRunning {
			continue
		}
		var (
			status string
			msg    pgtype.Text
		)
		err := tx.QueryRow(ctx, `SELECT status, error_message FROM ai_worker_jobs
			WHERE project_id = $1 AND location_id = $2 AND job_type = $3
			ORDER BY created_at DESC, id DESC LIMIT 1`,
			projectID, locationID, s.jobType).Scan(&status, &msg)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if status != "failed" {
			continue
		}
		reason := "reclaimed: worker restarted"
		if msg.Valid && msg.String != "" {
			reason = msg.String
		}
		if err := failStep(ctx, tx, setup, s.step, reason); err != nil {
			return err
		}
		setup.Steps[s.step] = StepFailed
	}
	if setup.AIAuditID.Valid && setup.Steps[StepAIVisibility] == StepRunning {
		var (
			status string
			msg    pgtype.Text
		)
		err := tx.QueryRow(ctx, `SELECT status, error_message FROM ai_worker_jobs
			WHERE audit_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, setup.AIAuditID).Scan(&status, &msg)
		if errors.Is(err, pgx.ErrNoRows) {
		} else if err != nil {
			return err
		} else if status == "failed" {
			reason := "reclaimed: worker restarted"
			if msg.Valid && msg.String != "" {
				reason = msg.String
			}
			if err := failStep(ctx, tx, setup, StepAIVisibility, reason); err != nil {
				return err
			}
			setup.Steps[StepAIVisibility] = StepFailed
		}
	}
	if setup.MapsRunID.Valid && setup.Steps[StepMapsVisibility] == StepRunning {
		var (
			status string
			msg    pgtype.Text
		)
		err := tx.QueryRow(ctx, `SELECT status, error_message FROM ai_worker_jobs
			WHERE local_run_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, setup.MapsRunID).Scan(&status, &msg)
		if errors.Is(err, pgx.ErrNoRows) {
		} else if err != nil {
			return err
		} else if status == "failed" {
			reason := "reclaimed: worker restarted"
			if msg.Valid && msg.String != "" {
				reason = msg.String
			}
			if err := failStep(ctx, tx, setup, StepMapsVisibility, reason); err != nil {
				return err
			}
			setup.Steps[StepMapsVisibility] = StepFailed
		}
	}
	return tx.Commit(ctx)
}
