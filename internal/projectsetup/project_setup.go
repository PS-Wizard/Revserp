// Package projectsetup owns the durable project setup workflow transitions
// shared by the crawl finalization paths and the owner's setup POST. It is
// deliberately a thin wrapper over conditional queries so each transition and
// its side effects can be exercised without a database.
package projectsetup

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/pgnull"
)

const (
	// StatusReady is the pre-start status a project's setup row is created in.
	// The owner's POST moves it to crawling; no worker runs while it is ready.
	StatusReady = "ready"
	// StatusCrawling is the only status the crawl-to-profile transition may
	// move away from.
	StatusCrawling = "crawling"
	// StatusProfileGeneration is the setup status after the initial crawl
	// completes; the profile worker then picks it up.
	StatusProfileGeneration = "profile_generation"
	// StatusPromptGeneration is the setup status while the prompt worker runs.
	StatusPromptGeneration = "prompt_generation"
	// StatusVisibility is the setup status while the first visibility audit runs.
	StatusVisibility = "visibility"
	// StatusCompleted is the terminal success status.
	StatusCompleted = "completed"
	// StatusFailed is the setup status after a linked step fails.
	StatusFailed = "failed"
	// FailedStepCrawling records that the initial crawl was the failed step.
	FailedStepCrawling = "crawling"
	// FailedStepProfileGeneration records the business profile step.
	FailedStepProfileGeneration = "profile_generation"
	// FailedStepPromptGeneration records the prompt generation step.
	FailedStepPromptGeneration = "prompt_generation"
	// FailedStepVisibility records the visibility audit step.
	FailedStepVisibility = "visibility"

	// BusinessProfileBootstrapJobType must match the ai_worker_jobs job_type the
	// profile worker registers (internal/aiaudit) and the database check
	// constraint on ai_worker_jobs.job_type.
	BusinessProfileBootstrapJobType = "business_profile_bootstrap"
	// PromptGenerationJobType must match the ai_worker_jobs job_type the prompt
	// worker registers (internal/aiaudit) and the database check constraint. A
	// failed-setup retry enqueues it for the prompt_generation and visibility
	// steps.
	PromptGenerationJobType = "prompt_generation"
)

// Retry errors. The HTTP handler maps ErrInvalidFailedStep and ErrResumeConflict
// to a 409 conflict and ErrSetupNotFound to 404.
var (
	// ErrSetupNotFound means no setup row exists for the project. A project
	// without a row predates the durable setup workflow and stays on manual
	// flows; the POST never creates a row for it.
	ErrSetupNotFound = errors.New("project setup not found")
	// ErrInvalidFailedStep means a failed setup has no resumable step, so the
	// retry refuses to guess where to restart.
	ErrInvalidFailedStep = errors.New("project setup failed step is missing or invalid")
	// ErrResumeConflict means the expected-status update matched no row because
	// another request advanced the setup first.
	ErrResumeConflict = errors.New("project setup resume conflict")
)

// Transitioner is the narrow database surface the crawl-to-profile transition
// needs. *sqlc.Queries satisfies it, including queries bound to a transaction
// through WithTx, so the setup transition and the bootstrap enqueue commit
// together with the crawl status write.
type Transitioner interface {
	AdvanceProjectSetupFromCrawling(ctx context.Context, arg sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error)
	EnqueueAIWorkerJob(ctx context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error)
}

// StartQueries is the narrow database surface starting or resuming a setup
// needs. *sqlc.Queries satisfies it, including a transaction-bound copy from
// WithTx, so the state change, the crawl and the retry job insert commit
// together in the request transaction.
type StartQueries interface {
	GetProjectSetupByProjectIDForUpdate(ctx context.Context, projectID pgtype.UUID) (sqlc.ProjectSetup, error)
	StartProjectSetup(ctx context.Context, arg sqlc.StartProjectSetupParams) (sqlc.ProjectSetup, error)
	UpdateProjectSetupStatus(ctx context.Context, arg sqlc.UpdateProjectSetupStatusParams) (sqlc.ProjectSetup, error)
	EnqueueAIWorkerJob(ctx context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error)
}

// StartOutcome reports the persisted setup row and whether this call started it
// by moving ready to crawling. The handler returns 201 for a start and 200 for
// a retry or an already-active no-op.
type StartOutcome struct {
	Setup   sqlc.ProjectSetup
	Started bool
}

// OnCrawlCompleted moves the setup linked to a completed crawl from crawling to
// profile_generation and enqueues exactly one business_profile_bootstrap job.
// The conditional transition returns pgx.ErrNoRows for an unrelated crawl or a
// duplicate terminal event, in which case nothing is enqueued.
func OnCrawlCompleted(ctx context.Context, q Transitioner, crawlID pgtype.UUID) error {
	setup, err := q.AdvanceProjectSetupFromCrawling(ctx, sqlc.AdvanceProjectSetupFromCrawlingParams{
		CrawlID: crawlID,
		Status:  StatusProfileGeneration,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("advance project setup from crawling: %w", err)
	}

	if _, err := q.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{
		JobType:   BusinessProfileBootstrapJobType,
		ProjectID: setup.ProjectID,
	}); err != nil {
		return fmt.Errorf("enqueue business profile bootstrap job: %w", err)
	}

	return nil
}

// OnCrawlFailed marks the setup linked to a failed or cancelled crawl as failed
// at the crawling step, copying the crawl error. The conditional transition
// returns pgx.ErrNoRows for an unrelated crawl or a duplicate terminal event.
func OnCrawlFailed(ctx context.Context, q Transitioner, crawlID pgtype.UUID, message string) error {
	_, err := q.AdvanceProjectSetupFromCrawling(ctx, sqlc.AdvanceProjectSetupFromCrawlingParams{
		CrawlID:    crawlID,
		Status:     StatusFailed,
		Error:      pgnull.Text(capSetupErrorMessage(message)),
		FailedStep: pgtype.Text{String: FailedStepCrawling, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail project setup for crawl: %w", err)
	}

	return nil
}

// StartOrResume locks the project's setup row and acts on its status:
//   - ready: stamp the acting owner, create the first crawl and move to crawling;
//   - failed: resume atomically at failed_step;
//   - active or completed: return the row unchanged and enqueue nothing.
//
// startCrawl creates the one new queued crawl and must run in the caller's
// transaction. It is called at most once, and only for the ready and crawling
// steps. A project with no setup row yields ErrSetupNotFound; the POST never
// creates one.
func StartOrResume(ctx context.Context, q StartQueries, projectID, requestedByUserID pgtype.UUID, startCrawl func(context.Context) (pgtype.UUID, error)) (StartOutcome, error) {
	setup, err := q.GetProjectSetupByProjectIDForUpdate(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StartOutcome{}, ErrSetupNotFound
	}
	if err != nil {
		return StartOutcome{}, fmt.Errorf("lock project setup: %w", err)
	}

	switch setup.Status {
	case StatusReady:
		crawlID, err := startCrawl(ctx)
		if err != nil {
			return StartOutcome{}, fmt.Errorf("start setup crawl: %w", err)
		}
		started, err := q.StartProjectSetup(ctx, sqlc.StartProjectSetupParams{
			RequestedByUserID: requestedByUserID,
			CrawlID:           crawlID,
			ProjectID:         projectID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return StartOutcome{}, ErrResumeConflict
		}
		if err != nil {
			return StartOutcome{}, fmt.Errorf("start project setup: %w", err)
		}
		return StartOutcome{Setup: started, Started: true}, nil

	case StatusFailed:
		updated, err := resumeFailedSetup(ctx, q, setup, requestedByUserID, startCrawl)
		if err != nil {
			return StartOutcome{}, err
		}
		return StartOutcome{Setup: updated}, nil

	default:
		// Active or completed: return the durable row unchanged and enqueue
		// nothing.
		return StartOutcome{Setup: setup}, nil
	}
}

// resumeFailedSetup moves a failed setup forward according to failed_step.
func resumeFailedSetup(ctx context.Context, q StartQueries, setup sqlc.ProjectSetup, requestedByUserID pgtype.UUID, startCrawl func(context.Context) (pgtype.UUID, error)) (sqlc.ProjectSetup, error) {
	failedStep := ""
	if setup.FailedStep.Valid {
		failedStep = setup.FailedStep.String
	}

	switch failedStep {
	case FailedStepCrawling:
		crawlID, err := startCrawl(ctx)
		if err != nil {
			return sqlc.ProjectSetup{}, fmt.Errorf("start retry crawl: %w", err)
		}
		return resumeSetup(ctx, q, setup.ProjectID, requestedByUserID, StatusCrawling, "", crawlID)
	case FailedStepProfileGeneration:
		return resumeSetup(ctx, q, setup.ProjectID, requestedByUserID, StatusProfileGeneration, BusinessProfileBootstrapJobType, pgtype.UUID{})
	case FailedStepPromptGeneration:
		return resumeSetup(ctx, q, setup.ProjectID, requestedByUserID, StatusPromptGeneration, PromptGenerationJobType, pgtype.UUID{})
	case FailedStepVisibility:
		// The simplest safe route: regenerate the questions and let the prompt
		// finalizer reserve quota and create a fresh audit. A failed audit is
		// never reused.
		return resumeSetup(ctx, q, setup.ProjectID, requestedByUserID, StatusPromptGeneration, PromptGenerationJobType, pgtype.UUID{})
	default:
		return sqlc.ProjectSetup{}, ErrInvalidFailedStep
	}
}

// resumeSetup performs the guarded state change and, when the step needs one,
// the single job enqueue. The expected-status guard returns ErrResumeConflict
// when a concurrent retry already moved the row, so no duplicate job or crawl
// is ever created. Passing an invalid VisibilitySkipReason clears any stale
// skip reason the failed attempt left behind.
func resumeSetup(ctx context.Context, q StartQueries, projectID, requestedByUserID pgtype.UUID, status, jobType string, crawlID pgtype.UUID) (sqlc.ProjectSetup, error) {
	updated, err := q.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		Status:               status,
		RequestedByUserID:    requestedByUserID,
		VisibilitySkipReason: pgtype.Text{},
		CrawlID:              crawlID,
		ProjectID:            projectID,
		ExpectedStatus:       StatusFailed,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ProjectSetup{}, ErrResumeConflict
	}
	if err != nil {
		return sqlc.ProjectSetup{}, fmt.Errorf("resume project setup: %w", err)
	}

	if jobType != "" {
		if _, err := q.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{
			JobType:   jobType,
			ProjectID: projectID,
		}); err != nil {
			return sqlc.ProjectSetup{}, fmt.Errorf("enqueue %s job: %w", jobType, err)
		}
	}
	return updated, nil
}

// maxSetupErrorMessageLength bounds the failure text persisted on the setup row
// and exposed to the UI. A mid-rune cut is avoided so the stored text stays
// valid UTF-8 and the transition never fails on an encoding error.
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
