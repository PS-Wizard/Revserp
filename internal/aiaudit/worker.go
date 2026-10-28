package aiaudit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/competitorsetup"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/locationsetup"
	"github.com/ps-wizard/revserp/internal/mapsvisibility"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

// Worker polls ai_worker_jobs and executes them.
type Worker struct {
	pool         *pgxpool.Pool
	queries      *sqlc.Queries
	cfg          config.Config
	concurrency  int
	pollInterval time.Duration

	// Web is the TinyFish-backed web search and fetch client used by the
	// bootstrap agent; nil leaves the web tools reporting an ordinary
	// unavailable state. Orchestration wires it when a key is configured.
	Web aichattools.WebClient

	visibilityQueries     visibilityQueries
	newVisibilityProvider func(modelSlug string) (ai.Provider, error)
	locationProfiles      LocationProfileReader
	locationQuestions     LocationAIQuestionReader
	locationTargets       LocationTargetReader
	locationLandmarks     LocationLandmarkReader
	newPromptProvider     func() (ai.Provider, error)
}

// New builds an AI worker.
func New(pool *pgxpool.Pool, cfg config.Config, concurrency int, pollInterval time.Duration) *Worker {
	if concurrency <= 0 {
		concurrency = 1
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	return &Worker{
		pool:              pool,
		queries:           sqlc.New(pool),
		cfg:               cfg,
		concurrency:       concurrency,
		pollInterval:      pollInterval,
		locationProfiles:  LocationProfileStore{Pool: pool},
		locationQuestions: LocationQuestionStore{Pool: pool},
		locationTargets:   LocationTargetStore{Pool: pool},
	}
}

// staleRunningJobAge is how long an ai_worker_jobs row (or an ai_audits row)
// may sit in 'running' before it is considered orphaned by a crashed worker
// process and reclaimed as failed. Age-gated (not an unconditional reset)
// because multiple worker replicas can run concurrently.
const staleRunningJobAge = 2 * time.Hour

// staleReclaimInterval is how often the periodic reclaim sweep runs, in
// addition to the once-at-startup reclaim below.
const staleReclaimInterval = 15 * time.Minute

// Run starts worker goroutines and blocks until the context is canceled.
func (w *Worker) Run(ctx context.Context) error {
	w.reclaimStale(ctx)

	go w.runStaleReclaimLoop(ctx)
	go w.runCompetitorReconcileLoop(ctx)

	done := make(chan struct{}, w.concurrency)
	for i := range w.concurrency {
		go func() {
			w.runLoop(ctx, i+1)
			done <- struct{}{}
		}()
	}
	for range w.concurrency {
		<-done
	}
	return nil
}

// runStaleReclaimLoop periodically reclaims stale-running ai_worker_jobs and
// ai_audits rows so orphaned work recovers without waiting for a restart.
func (w *Worker) runStaleReclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(staleReclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reclaimStale(ctx)
		}
	}
}

// runCompetitorReconcileLoop advances competitor setups whose crawl step waits
// on a child crawl. It runs on its own ticker rather than on idle claim ticks,
// so a busy job queue cannot hold a finished crawl in "running" for minutes.
func (w *Worker) runCompetitorReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reconcileCompetitorSetups(ctx)
		}
	}
}

// reclaimStale fails orphaned running rows. Reclaiming ai_worker_jobs also
// fails the matching active setup for setup-linked job types in the same
// statement, so a crashed worker cannot leave a setup active forever.
func (w *Worker) reclaimStale(ctx context.Context) {
	cutoff := pgtype.Timestamptz{Time: time.Now().UTC().Add(-staleRunningJobAge), Valid: true}

	if err := w.queries.ReclaimStaleRunningAIWorkerJobs(ctx, cutoff); err != nil {
		log.Printf("failed to reclaim stale running ai worker jobs: %v", err)
	}
	if err := w.queries.ReclaimStaleRunningAIAudits(ctx, cutoff); err != nil {
		log.Printf("failed to reclaim stale running ai audits: %v", err)
	}
	runs, err := w.queries.ListInterruptedLocalVisibilityRuns(ctx)
	if err != nil {
		log.Printf("failed to list interrupted local visibility runs: %v", err)
		return
	}
	store := localvisibility.LocalVisibilityStore{Pool: w.pool}
	for _, runID := range runs {
		if err := store.FinishRun(ctx, runID); err != nil {
			log.Printf("failed to settle interrupted local visibility run %s: %v", runID.String(), err)
		}
	}
	if err := locationsetup.ReclaimSweep(ctx, w.pool); err != nil {
		log.Printf("failed to reclaim stale location setups: %v", err)
	}
	if err := locationsetup.ReconcileRunningSetups(ctx, w.pool, w.cfg.SerperMapsEndpoint); err != nil {
		log.Printf("failed to reconcile running location setups: %v", err)
	}
}

func (w *Worker) runLoop(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := w.queries.ClaimNextPendingAIWorkerJob(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if sleepErr := sleepOrCancel(ctx, w.pollInterval); sleepErr != nil {
					return
				}
				continue
			}
			log.Printf("ai worker %d: claim error: %v", workerID, err)
			if sleepErr := sleepOrCancel(ctx, w.pollInterval); sleepErr != nil {
				return
			}
			continue
		}

		log.Printf("ai worker %d: claimed job id=%s type=%s project=%s", workerID, job.ID.String(), job.JobType, job.ProjectID.String())

		var jobErr error
		var visibilityStatus string
		switch job.JobType {
		case promptGenerationJobType:
			jobErr = w.handlePromptGeneration(ctx, job)
		case visibilityRunJobType:
			visibilityStatus, jobErr = w.handleVisibilityRun(ctx, job)
		case mapsVisibilityJobType:
			jobErr = mapsvisibility.HandleMapsVisibilityCheck(ctx, w.queries, w.cfg, job.ProjectID)
		case localvisibility.LocalVisibilityJobType:
			jobErr = localvisibility.ExecuteLocalVisibilityRun(ctx, w.pool, w.cfg, job.LocalRunID, job.ProjectID)
		case LocationProfileGenerationJobType:
			jobErr = w.handleLocationProfileGeneration(ctx, job)
		case LocationMapQueryGenerationJobType:
			jobErr = w.handleLocationMapQueryGeneration(ctx, job)
		case LocationKeywordAnalysisJobType:
			jobErr = w.handleLocationKeywordAnalysis(ctx, job)
		case projectsetup.BusinessProfileBootstrapJobType:
			jobErr = w.handleBusinessProfileBootstrap(ctx, job)
		case projectsetup.KeywordAnalysisJobType:
			jobErr = w.handleKeywordAnalysis(ctx, job)
		case competitorsetup.CompetitorKeywordGenerationJobType:
			jobErr = w.handleCompetitorKeywordGeneration(ctx, job)
		case competitorsetup.CompetitorKeywordAnalysisJobType:
			jobErr = w.handleCompetitorKeywordAnalysis(ctx, job)
		default:
			jobErr = fmt.Errorf("unknown job type: %s", job.JobType)
		}

		if jobErr != nil {
			log.Printf("ai worker %d: job %s failed: %v", workerID, job.ID.String(), jobErr)
			if finalizeErr := w.finalizeJobFailure(ctx, job, jobErr); finalizeErr != nil {
				log.Printf("ai worker %d: finalize failure error: %v", workerID, finalizeErr)
			}
			continue
		}

		if finalizeErr := w.finalizeJobSuccess(ctx, job, visibilityStatus); finalizeErr != nil {
			log.Printf("ai worker %d: finalize success error: %v", workerID, finalizeErr)
			continue
		}
		log.Printf("ai worker %d: job %s completed", workerID, job.ID.String())
	}
}

// finalizeJobSuccess writes the terminal success state. Setup-linked jobs do it
// in one transaction with the matching setup transition; every other job keeps
// the plain completion.
func (w *Worker) finalizeJobSuccess(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow, visibilityStatus string) error {
	switch job.JobType {
	case projectsetup.BusinessProfileBootstrapJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeBootstrapSuccess(ctx, q, job)
		})
	case projectsetup.KeywordAnalysisJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeKeywordAnalysisSuccess(ctx, q, job)
		})
	case competitorsetup.CompetitorKeywordGenerationJobType:
		if !job.CompetitorID.Valid {
			return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
				ID:           job.ID,
				ErrorMessage: pgtype.Text{String: "competitor job has no competitor", Valid: true},
			})
		}
		return w.withCompetitorSetupTx(ctx, func(q competitorsetup.Queries) error {
			return competitorsetup.FinalizeGenerationSuccess(ctx, q, job)
		})
	case competitorsetup.CompetitorKeywordAnalysisJobType:
		if !job.CompetitorID.Valid {
			return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
				ID:           job.ID,
				ErrorMessage: pgtype.Text{String: "competitor job has no competitor", Valid: true},
			})
		}
		return w.withCompetitorSetupTx(ctx, func(q competitorsetup.Queries) error {
			return competitorsetup.FinalizeAnalysisSuccess(ctx, q, job)
		})
	case promptGenerationJobType:
		if w.isLocationPromptJob(job) {
			if locationID, ok := locationSetupScope(job); ok {
				return locationsetup.FinalizeQuestionsSuccess(ctx, w.pool, job.ProjectID, locationID, job.ID)
			}
			return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
		}
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizePromptGenerationSuccess(ctx, q, job)
		})
	case visibilityRunJobType:
		isLocation, scopeErr := w.isLocationVisibilityJob(ctx, job)
		if scopeErr != nil {
			return scopeErr
		}
		if isLocation {
			if auditID, ok := locationSetupAuditID(job); ok {
				return locationsetup.FinalizeVisibilityRun(ctx, w.pool, job.ID, auditID, visibilityStatus, nil)
			}
			return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
		}
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeVisibilitySuccess(ctx, q, job, visibilityStatus)
		})
	case localvisibility.LocalVisibilityJobType:
		if runID, ok := locationSetupRunID(job); ok {
			return locationsetup.FinalizeLocalRun(ctx, w.pool, job.ID, runID, nil)
		}
		return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
	case LocationProfileGenerationJobType:
		if locationID, ok := locationSetupScope(job); ok {
			return locationsetup.FinalizeProfileSuccess(ctx, w.pool, job.ProjectID, locationID, job.ID)
		}
		return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
	case LocationMapQueryGenerationJobType:
		if locationID, ok := locationSetupScope(job); ok {
			return locationsetup.FinalizeMapQueriesSuccess(ctx, w.pool, job.ProjectID, locationID, job.ID, w.cfg.SerperMapsEndpoint)
		}
		return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
	case LocationKeywordAnalysisJobType:
		if locationID, ok := locationSetupScope(job); ok {
			return locationsetup.FinalizeKeywordAnalysisSuccess(ctx, w.pool, job.ProjectID, locationID, job.ID)
		}
		return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
	default:
		return w.queries.MarkAIWorkerJobCompleted(ctx, job.ID)
	}
}

// finalizeJobFailure writes the terminal failure state. Setup-linked jobs also
// fail the matching setup step in the same transaction; every other job keeps
// the plain failure.
func (w *Worker) finalizeJobFailure(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow, jobErr error) error {
	message := jobErr.Error()
	switch job.JobType {
	case projectsetup.KeywordAnalysisJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeKeywordAnalysisFailure(ctx, q, job, message)
		})
	case competitorsetup.CompetitorKeywordGenerationJobType:
		return w.withCompetitorSetupTx(ctx, func(q competitorsetup.Queries) error {
			if err := markCompetitorJobFailed(ctx, q, job.ID, message); err != nil {
				return err
			}
			return competitorsetup.FinalizeStepFailure(ctx, q, job.CompetitorID, competitorsetup.StepKeywordGeneration, message)
		})
	case competitorsetup.CompetitorKeywordAnalysisJobType:
		return w.withCompetitorSetupTx(ctx, func(q competitorsetup.Queries) error {
			if err := markCompetitorJobFailed(ctx, q, job.ID, message); err != nil {
				return err
			}
			return competitorsetup.FinalizeStepFailure(ctx, q, job.CompetitorID, competitorsetup.StepKeywordAnalysis, message)
		})
	case projectsetup.BusinessProfileBootstrapJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeBootstrapFailure(ctx, q, job, message)
		})
	case promptGenerationJobType:
		if w.isLocationPromptJob(job) {
			if locationID, ok := locationSetupScope(job); ok {
				return locationsetup.FinalizeQuestionsFailure(ctx, w.pool, job.ProjectID, locationID, job.ID, message)
			}
			return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
				ID:           job.ID,
				ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
			})
		}
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizePromptGenerationFailure(ctx, q, job, message)
		})
	case visibilityRunJobType:
		isLocation, scopeErr := w.isLocationVisibilityJob(ctx, job)
		if scopeErr != nil {
			return scopeErr
		}
		if isLocation {
			if auditID, ok := locationSetupAuditID(job); ok {
				return locationsetup.FinalizeVisibilityRun(ctx, w.pool, job.ID, auditID, "", errors.New(message))
			}
			return w.failLocationVisibilityJob(ctx, job, message)
		}
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeVisibilityFailure(ctx, q, job, message)
		})
	case localvisibility.LocalVisibilityJobType:
		if runID, ok := locationSetupRunID(job); ok {
			return locationsetup.FinalizeLocalRun(ctx, w.pool, job.ID, runID, errors.New(message))
		}
		return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
		})
	case LocationProfileGenerationJobType:
		if locationID, ok := locationSetupScope(job); ok {
			return locationsetup.FinalizeProfileFailure(ctx, w.pool, job.ProjectID, locationID, job.ID, message)
		}
		return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
		})
	case LocationMapQueryGenerationJobType:
		if locationID, ok := locationSetupScope(job); ok {
			return locationsetup.FinalizeMapQueriesFailure(ctx, w.pool, job.ProjectID, locationID, job.ID, message)
		}
		return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
		})
	case LocationKeywordAnalysisJobType:
		if errors.Is(jobErr, ErrLocationKeywordsEmpty) {
			if locationID, ok := locationSetupScope(job); ok {
				return locationsetup.FinalizeKeywordAnalysisEmpty(ctx, w.pool, job.ProjectID, locationID, job.ID)
			}
		}
		if locationID, ok := locationSetupScope(job); ok {
			return locationsetup.FinalizeKeywordAnalysisFailure(ctx, w.pool, job.ProjectID, locationID, job.ID, message)
		}
		return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
		})
	default:
		return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
		})
	}
}

// markCompetitorJobFailed marks one competitor job failed inside the caller's
// competitor setup transaction.
func markCompetitorJobFailed(ctx context.Context, q competitorsetup.Queries, jobID pgtype.UUID, message string) error {
	if err := q.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
		ID:           jobID,
		ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
	}); err != nil {
		return fmt.Errorf("mark competitor job failed: %w", err)
	}
	return nil
}

// withCompetitorSetupTx runs fn against a transaction-bound competitor setup
// querier and commits only when fn returns nil.
func (w *Worker) withCompetitorSetupTx(ctx context.Context, fn func(q competitorsetup.Queries) error) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin competitor setup finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(w.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit competitor setup finalization: %w", err)
	}
	return nil
}

// reconcileCompetitorSetups advances every running competitor setup whose
// crawl step still waits on its child crawl. Each setup reconciles in its own
// transaction from the stored parent, reusing the same transition the POST
// path uses; a completed child enqueues keyword generation, a failed child
// fails the setup at crawl.
func (w *Worker) reconcileCompetitorSetups(ctx context.Context) {
	ids, err := w.queries.ListCompetitorSetupsCrawlRunning(ctx)
	if err != nil {
		log.Printf("ai worker: list competitor setups for reconcile: %v", err)
		return
	}
	for _, id := range ids {
		if err := w.withCompetitorSetupTx(ctx, func(q competitorsetup.Queries) error {
			_, err := competitorsetup.Reconcile(ctx, q, id)
			return err
		}); err != nil {
			log.Printf("ai worker: reconcile competitor setup %s: %v", id.String(), err)
		}
	}
}

// withSetupTx runs fn against a transaction-bound querier and commits only when
// fn returns nil, so each finalization is atomic.
func (w *Worker) withSetupTx(ctx context.Context, fn func(q setupFinalizationQueries) error) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin setup finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(w.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit setup finalization: %w", err)
	}
	return nil
}

func sleepOrCancel(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
