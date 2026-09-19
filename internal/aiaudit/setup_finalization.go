package aiaudit

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/pgnull"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

// Job types the AI worker dispatches. BusinessProfileBootstrapJobType lives in
// projectsetup because the crawl finalizer enqueues it; the rest are owned here.
const (
	// prompt generation is enqueued both by setup finalization here and by the
	// failed-setup retry, so the job type is shared from projectsetup.
	promptGenerationJobType = projectsetup.PromptGenerationJobType
	visibilityRunJobType    = "visibility_run"
	mapsVisibilityJobType   = "maps_visibility"
)

// Visibility skip reasons surfaced on the setup row when the prompt step cannot
// start a visibility audit. They are the only statuses where setup completes
// without a visibility audit.
const (
	visibilityDisabledSkipReason = "visibility audits are disabled for this workspace"
	visibilityQuotaSkipReason    = "visibility audit monthly limit reached"
	visibilityAuditFailedMessage = "visibility audit failed"
)

// setupFinalizationQueries is the narrow query surface a setup-linked job
// finalization needs. *sqlc.Queries satisfies it, including a transaction-bound
// copy from WithTx, so the whole finalization runs in one transaction and the
// same functions are exercisable with a fake.
type setupFinalizationQueries interface {
	GetProjectSetupByProjectIDForUpdate(ctx context.Context, projectID pgtype.UUID) (sqlc.ProjectSetup, error)
	UpdateProjectSetupStatus(ctx context.Context, arg sqlc.UpdateProjectSetupStatusParams) (sqlc.ProjectSetup, error)
	EnqueueAIWorkerJob(ctx context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error)
	MarkAIWorkerJobCompleted(ctx context.Context, id pgtype.UUID) error
	MarkAIWorkerJobFailed(ctx context.Context, arg sqlc.MarkAIWorkerJobFailedParams) error
	GetOrganizationFeaturesByProjectID(ctx context.Context, arg sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error)
	ReserveAIWorkspaceMonthlyAudit(ctx context.Context, arg sqlc.ReserveAIWorkspaceMonthlyAuditParams) (int32, error)
	FailActiveAIAuditsForCrawl(ctx context.Context, arg sqlc.FailActiveAIAuditsForCrawlParams) (int64, error)
	CreateAIAudit(ctx context.Context, arg sqlc.CreateAIAuditParams) (sqlc.AiAudit, error)
}

// finalizeBootstrapSuccess moves the setup from profile_generation to
// prompt_generation, enqueues exactly one prompt_generation job, and marks the
// bootstrap job completed. The conditional update is the duplicate guard: only
// the transaction that wins the transition enqueues, so a duplicate or an
// already-advanced bootstrap never adds a second job. A recovery run whose
// profile already exists still advances the setup, which is the whole point.
// A job with no setup (no row) falls back to plain completion.
func finalizeBootstrapSuccess(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	setup, err := q.GetProjectSetupByProjectIDForUpdate(ctx, job.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}
	if err != nil {
		return fmt.Errorf("lock project setup for bootstrap: %w", err)
	}

	if setup.Status == projectsetup.StatusProfileGeneration {
		_, err := q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
			Status:         projectsetup.StatusPromptGeneration,
			ProjectID:      setup.ProjectID,
			ExpectedStatus: projectsetup.StatusProfileGeneration,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Another transaction already advanced this setup.
		case err != nil:
			return fmt.Errorf("advance setup to prompt generation: %w", err)
		default:
			if _, err := q.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{
				JobType:   promptGenerationJobType,
				ProjectID: setup.ProjectID,
			}); err != nil {
				return fmt.Errorf("enqueue prompt generation job: %w", err)
			}
		}
	}

	return q.MarkAIWorkerJobCompleted(ctx, job.ID)
}

// finalizeBootstrapFailure marks the bootstrap job failed and fails the setup at
// profile_generation only while it is still on that step.
func finalizeBootstrapFailure(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, message string) error {
	if err := markJobFailed(ctx, q, job.ID, message); err != nil {
		return err
	}
	return failSetupAtStep(ctx, q, job.ProjectID, projectsetup.StatusProfileGeneration, projectsetup.FailedStepProfileGeneration, message)
}

// finalizePromptGenerationSuccess decides what the setup does after questions are
// generated, all in the caller's transaction: complete with a skip reason when
// visibility is disabled or the quota is exhausted, otherwise create the queued
// audit for the setup crawl, enqueue visibility_run, and move the setup to
// visibility. The expected-current-status guard plus the row lock make a
// duplicate prompt job a plain completion instead of a second audit. A manual
// prompt job with no setup, or a setup not on prompt_generation, is completed
// without touching setup.
func finalizePromptGenerationSuccess(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	setup, err := q.GetProjectSetupByProjectIDForUpdate(ctx, job.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}
	if err != nil {
		return fmt.Errorf("lock project setup for prompt generation: %w", err)
	}
	if setup.Status != projectsetup.StatusPromptGeneration {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}

	// Feature authorization uses the setup requester, exactly like the manual
	// audit handler uses the acting member.
	features, err := q.GetOrganizationFeaturesByProjectID(ctx, sqlc.GetOrganizationFeaturesByProjectIDParams{
		UserID:    setup.RequestedByUserID,
		ProjectID: setup.ProjectID,
	})
	if err != nil {
		return fmt.Errorf("load organization features for visibility: %w", err)
	}

	if features.AiVisibilityAuditMonthlyLimit <= 0 {
		return completeSetupWithVisibilitySkip(ctx, q, job, setup, visibilityDisabledSkipReason)
	}

	if _, err := q.ReserveAIWorkspaceMonthlyAudit(ctx, sqlc.ReserveAIWorkspaceMonthlyAuditParams{
		OrganizationID: setup.OrganizationID,
		MonthlyLimit:   features.AiVisibilityAuditMonthlyLimit,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return completeSetupWithVisibilitySkip(ctx, q, job, setup, visibilityQuotaSkipReason)
		}
		return fmt.Errorf("reserve visibility audit: %w", err)
	}

	// A queued/running audit orphaned for this crawl (for example a visibility
	// job that crashed mid-run) would collide with the fresh audit on the
	// one-active-per-crawl index. Fail only those active rows so the retry gets
	// a clean audit; terminal history is never touched.
	if _, err := q.FailActiveAIAuditsForCrawl(ctx, sqlc.FailActiveAIAuditsForCrawlParams{
		ProjectID: setup.ProjectID,
		CrawlID:   setup.CrawlID,
	}); err != nil {
		return fmt.Errorf("clear active visibility audits: %w", err)
	}

	audit, err := q.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{
		ProjectID: setup.ProjectID,
		CrawlID:   setup.CrawlID,
		Status:    "queued",
	})
	if err != nil {
		return fmt.Errorf("create visibility audit: %w", err)
	}
	if _, err := q.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{
		JobType:   visibilityRunJobType,
		ProjectID: setup.ProjectID,
		AuditID:   pgtype.UUID{Bytes: audit.ID.Bytes, Valid: true},
	}); err != nil {
		return fmt.Errorf("enqueue visibility run job: %w", err)
	}
	if _, err := q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		Status:         projectsetup.StatusVisibility,
		ProjectID:      setup.ProjectID,
		ExpectedStatus: projectsetup.StatusPromptGeneration,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Should not happen under the row lock; treat as already advanced.
			return q.MarkAIWorkerJobCompleted(ctx, job.ID)
		}
		return fmt.Errorf("advance setup to visibility: %w", err)
	}

	return q.MarkAIWorkerJobCompleted(ctx, job.ID)
}

// completeSetupWithVisibilitySkip completes the setup with a skip reason and
// marks the prompt job completed.
func completeSetupWithVisibilitySkip(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, setup sqlc.ProjectSetup, reason string) error {
	_, err := q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		Status:               projectsetup.StatusCompleted,
		VisibilitySkipReason: pgtype.Text{String: reason, Valid: true},
		ProjectID:            setup.ProjectID,
		ExpectedStatus:       projectsetup.StatusPromptGeneration,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("complete setup with visibility skip: %w", err)
	}
	return q.MarkAIWorkerJobCompleted(ctx, job.ID)
}

// finalizePromptGenerationFailure marks the prompt job failed and fails the setup
// at prompt_generation only while it is still on that step.
func finalizePromptGenerationFailure(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, message string) error {
	if err := markJobFailed(ctx, q, job.ID, message); err != nil {
		return err
	}
	return failSetupAtStep(ctx, q, job.ProjectID, projectsetup.StatusPromptGeneration, projectsetup.FailedStepPromptGeneration, message)
}

// finalizeVisibilitySuccess completes the setup for a finished audit, or fails it
// at visibility when every audit run failed. A manual visibility job with no
// setup, or a setup not on visibility, is completed without touching setup.
func finalizeVisibilitySuccess(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, auditStatus string) error {
	setup, err := q.GetProjectSetupByProjectIDForUpdate(ctx, job.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}
	if err != nil {
		return fmt.Errorf("lock project setup for visibility: %w", err)
	}
	if setup.Status != projectsetup.StatusVisibility {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}

	if auditStatus == "failed" {
		_, err := q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
			Status:         projectsetup.StatusFailed,
			Error:          pgnull.Text(visibilityAuditFailedMessage),
			FailedStep:     pgtype.Text{String: projectsetup.FailedStepVisibility, Valid: true},
			ProjectID:      setup.ProjectID,
			ExpectedStatus: projectsetup.StatusVisibility,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("fail setup at visibility: %w", err)
		}
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}

	_, err = q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		Status:         projectsetup.StatusCompleted,
		ProjectID:      setup.ProjectID,
		ExpectedStatus: projectsetup.StatusVisibility,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("complete setup at visibility: %w", err)
	}
	return q.MarkAIWorkerJobCompleted(ctx, job.ID)
}

// finalizeVisibilityFailure marks the visibility job failed and fails the setup
// at visibility only while it is still on that step.
func finalizeVisibilityFailure(ctx context.Context, q setupFinalizationQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, message string) error {
	if err := markJobFailed(ctx, q, job.ID, message); err != nil {
		return err
	}
	return failSetupAtStep(ctx, q, job.ProjectID, projectsetup.StatusVisibility, projectsetup.FailedStepVisibility, message)
}

func markJobFailed(ctx context.Context, q setupFinalizationQueries, jobID pgtype.UUID, message string) error {
	if err := q.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
		ID:           jobID,
		ErrorMessage: pgnull.Text(capSetupErrorMessage(message)),
	}); err != nil {
		return fmt.Errorf("mark ai worker job failed: %w", err)
	}
	return nil
}

// failSetupAtStep fails the setup only when it is still on expectedStatus, so a
// stale job cannot move an already-advanced setup backward.
func failSetupAtStep(ctx context.Context, q setupFinalizationQueries, projectID pgtype.UUID, expectedStatus, failedStep, message string) error {
	_, err := q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		Status:         projectsetup.StatusFailed,
		Error:          pgnull.Text(capSetupErrorMessage(message)),
		FailedStep:     pgtype.Text{String: failedStep, Valid: true},
		ProjectID:      projectID,
		ExpectedStatus: expectedStatus,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("fail project setup: %w", err)
	}
	return nil
}

// maxSetupErrorMessageLength bounds the failure text persisted on the setup row
// and exposed to the UI. A mid-rune cut keeps the stored text valid UTF-8.
const maxSetupErrorMessageLength = 500

func capSetupErrorMessage(message string) string {
	if len(message) <= maxSetupErrorMessageLength {
		return message
	}
	cut := maxSetupErrorMessageLength
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut]
}
