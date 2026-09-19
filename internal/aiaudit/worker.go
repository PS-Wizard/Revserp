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

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
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
		pool:         pool,
		queries:      sqlc.New(pool),
		cfg:          cfg,
		concurrency:  concurrency,
		pollInterval: pollInterval,
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
		case projectsetup.BusinessProfileBootstrapJobType:
			jobErr = w.handleBusinessProfileBootstrap(ctx, job)
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
	case promptGenerationJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizePromptGenerationSuccess(ctx, q, job)
		})
	case visibilityRunJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeVisibilitySuccess(ctx, q, job, visibilityStatus)
		})
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
	case projectsetup.BusinessProfileBootstrapJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeBootstrapFailure(ctx, q, job, message)
		})
	case promptGenerationJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizePromptGenerationFailure(ctx, q, job, message)
		})
	case visibilityRunJobType:
		return w.withSetupTx(ctx, func(q setupFinalizationQueries) error {
			return finalizeVisibilityFailure(ctx, q, job, message)
		})
	default:
		return w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capSetupErrorMessage(message), Valid: true},
		})
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
