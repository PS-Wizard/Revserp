package aiaudit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Worker polls for queued AI audits and executes them.
type Worker struct {
	pool         *pgxpool.Pool
	concurrency  int
	pollInterval time.Duration
}

// New builds an AI audit worker.
func New(pool *pgxpool.Pool, concurrency int, pollInterval time.Duration) *Worker {
	if concurrency <= 0 {
		concurrency = 1
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}

	return &Worker{
		pool:         pool,
		concurrency:  concurrency,
		pollInterval: pollInterval,
	}
}

// Run starts worker loops until the context is canceled.
func (worker *Worker) Run(ctx context.Context) error {
	errorsChannel := make(chan error, worker.concurrency)
	for workerIndex := range worker.concurrency {
		go func() {
			errorsChannel <- worker.runLoop(ctx, workerIndex+1)
		}()
	}

	for range worker.concurrency {
		if err := <-errorsChannel; err != nil {
			return err
		}
	}

	return nil
}

// runLoop keeps the process alive until AI audit job polling is implemented.
func (worker *Worker) runLoop(ctx context.Context, workerID int) error {
	_ = workerID
	for {
		if err := sleepOrCancel(ctx, worker.pollInterval); err != nil {
			return nil
		}
	}
}

// sleepOrCancel sleeps for a poll interval or returns when context is canceled.
func sleepOrCancel(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
		return nil
	}
}
