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

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Worker polls ai_worker_jobs and executes them.
type Worker struct {
	pool         *pgxpool.Pool
	queries      *sqlc.Queries
	cfg          config.Config
	concurrency  int
	pollInterval time.Duration
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

// Run starts worker goroutines and blocks until the context is canceled.
func (w *Worker) Run(ctx context.Context) error {
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
		switch job.JobType {
		case "prompt_generation":
			jobErr = w.handlePromptGeneration(ctx, job)
		default:
			jobErr = fmt.Errorf("unknown job type: %s", job.JobType)
		}

		if jobErr != nil {
			log.Printf("ai worker %d: job %s failed: %v", workerID, job.ID.String(), jobErr)
			if markErr := w.queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
				ID:           job.ID,
				ErrorMessage: pgtype.Text{String: jobErr.Error(), Valid: true},
			}); markErr != nil {
				log.Printf("ai worker %d: mark failed error: %v", workerID, markErr)
			}
			continue
		}

		if markErr := w.queries.MarkAIWorkerJobCompleted(ctx, job.ID); markErr != nil {
			log.Printf("ai worker %d: mark completed error: %v", workerID, markErr)
		}
		log.Printf("ai worker %d: job %s completed", workerID, job.ID.String())
	}
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
