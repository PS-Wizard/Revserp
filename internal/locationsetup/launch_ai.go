package locationsetup

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// LaunchAIVisibility reserves one workspace audit and starts the location AI
// test. Quota exhaustion, a disabled feature, or a departed requester skips
// the step; an active audit the setup already owns is adopted, never refailed.
func LaunchAIVisibility(ctx context.Context, pool *pgxpool.Pool, projectID, locationID pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if err != nil {
		return err
	}
	if err := launchAIVisibilityTx(ctx, tx, setup); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func launchAIVisibilityTx(ctx context.Context, tx pgx.Tx, setup Setup) error {
	if setup.Steps[StepAIVisibility] != StepPending || setup.Steps[StepQuestions] != StepCompleted {
		return nil
	}
	if setup.AIAuditID.Valid {
		var auditStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM ai_audits WHERE id = $1`, setup.AIAuditID).Scan(&auditStatus); err == nil {
			switch auditStatus {
			case "completed", "completed_with_failures":
				return completeStep(ctx, tx, setup, StepAIVisibility)
			case "queued", "running":
				if err := ensureAuditJob(ctx, tx, setup, setup.AIAuditID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE location_setup SET ai_visibility_status = 'running', updated_at = now()
					WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID)
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM project_locations WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE`, setup.LocationID, setup.ProjectID); err != nil {
		return err
	}
	var foreignActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ai_audits
		WHERE project_id = $1 AND location_id = $2 AND status IN ('queued', 'running'))`,
		setup.ProjectID, setup.LocationID).Scan(&foreignActive); err != nil {
		return err
	}
	if foreignActive {
		return deferStep(ctx, tx, setup, StepAIVisibility, "a visibility audit is already in progress for this location")
	}
	var questionCount int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_array_length(questions), 0) FROM location_ai_questions
		WHERE location_id = $1 AND project_id = $2`, setup.LocationID, setup.ProjectID).Scan(&questionCount); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if questionCount == 0 {
		return failStep(ctx, tx, setup, StepQuestions, "saved location questions are empty")
	}
	q := sqlc.New(tx)
	features, err := q.GetOrganizationFeaturesByProjectID(ctx, sqlc.GetOrganizationFeaturesByProjectIDParams{
		UserID:    setup.RequestedByUserID,
		ProjectID: setup.ProjectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return skipStep(ctx, tx, setup, StepAIVisibility, SkipAIVisibilityAccess)
	}
	if err != nil {
		return err
	}
	if features.AiVisibilityAuditMonthlyLimit <= 0 {
		return skipStep(ctx, tx, setup, StepAIVisibility, SkipAIVisibilityDisabled)
	}
	if _, err := q.ReserveAIWorkspaceMonthlyAudit(ctx, sqlc.ReserveAIWorkspaceMonthlyAuditParams{
		OrganizationID: setup.OrganizationID,
		MonthlyLimit:   features.AiVisibilityAuditMonthlyLimit,
	}); errors.Is(err, pgx.ErrNoRows) {
		return skipStep(ctx, tx, setup, StepAIVisibility, SkipAIVisibilityQuota)
	} else if err != nil {
		return err
	}
	var auditID pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO ai_audits (project_id, location_id, status)
		VALUES ($1, $2, 'queued') RETURNING id`, setup.ProjectID, setup.LocationID).Scan(&auditID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE location_setup SET ai_audit_id = $3, updated_at = now()
		WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID, auditID); err != nil {
		return err
	}
	setup.AIAuditID = auditID
	if err := ensureAuditJob(ctx, tx, setup, auditID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE location_setup SET ai_visibility_status = 'running', updated_at = now()
		WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID)
	return err
}

func ensureAuditJob(ctx context.Context, tx pgx.Tx, setup Setup, auditID pgtype.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ai_worker_jobs
		WHERE audit_id = $1 AND status IN ('pending', 'running'))`, auditID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, audit_id, status)
		VALUES ('visibility_run', $1, $2, $3, 'pending') ON CONFLICT DO NOTHING`,
		setup.ProjectID, setup.LocationID, auditID)
	return err
}

// FinalizeQuestionsSuccess completes the questions step and launches the AI
// test in the same transaction, so no crash gap can strand it.
func FinalizeQuestionsSuccess(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID) error {
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
	if setup.Steps[StepQuestions] != StepRunning {
		return tx.Commit(ctx)
	}
	if err := completeStep(ctx, tx, setup, StepQuestions); err != nil {
		return err
	}
	if err := launchAIVisibilityTx(ctx, tx, setup); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinalizeQuestionsFailure fails the questions step. Other branches still
// complete on their own; the POST resume relaunches only unfinished work.
func FinalizeQuestionsFailure(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID, message string) error {
	return failOwnedJob(ctx, pool, projectID, locationID, jobID, StepQuestions, message)
}

// FinalizeVisibilityRun records the AI test outcome. Only the audit the setup
// launched advances the step; anything else only settles the job and audit.
func FinalizeVisibilityRun(ctx context.Context, pool *pgxpool.Pool, jobID, auditID pgtype.UUID, auditStatus string, jobErr error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if jobErr != nil {
		if err := markJobFailed(ctx, tx, jobID, jobErr.Error()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ai_audits SET status = 'failed', error_message = $2,
			completed_at = NOW(), updated_at = NOW()
			WHERE id = $1 AND status IN ('queued', 'running')`, auditID, cutError(jobErr.Error())); err != nil {
			return err
		}
	} else if err := markJobCompleted(ctx, tx, jobID); err != nil {
		return err
	}
	var scope struct {
		projectID  pgtype.UUID
		locationID pgtype.UUID
	}
	if err := tx.QueryRow(ctx, `SELECT project_id, location_id FROM ai_audits WHERE id = $1`,
		auditID).Scan(&scope.projectID, &scope.locationID); err != nil {
		return tx.Commit(ctx)
	}
	if !scope.locationID.Valid {
		return tx.Commit(ctx)
	}
	setup, err := getSetupTx(ctx, tx, scope.projectID, scope.locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[StepAIVisibility] != StepRunning || !setup.AIAuditID.Valid || setup.AIAuditID != auditID {
		return tx.Commit(ctx)
	}
	if jobErr != nil || auditStatus == "failed" {
		msg := "visibility audit failed"
		if jobErr != nil {
			msg = jobErr.Error()
		}
		if err := failStep(ctx, tx, setup, StepAIVisibility, msg); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := completeStep(ctx, tx, setup, StepAIVisibility); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
