package aiaudit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Worker polls for queued AI audits and executes them.
// Queue processing is not yet implemented; Run returns immediately.
type Worker struct {
	pool         *pgxpool.Pool
	concurrency  int
	pollInterval time.Duration // kept for future use, currently unused
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

// Run returns immediately. AI audit queue processing is not yet implemented.
// The worker exists to keep the deployment topology consistent until real
// queue polling and processing is added.
func (worker *Worker) Run(ctx context.Context) error {
	return nil
}
