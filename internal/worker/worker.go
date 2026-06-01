package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Worker claims queued crawls from Postgres and runs them.
type Worker struct {
	pool         *pgxpool.Pool
	queries      *sqlc.Queries
	concurrency  int
	pollInterval time.Duration
}

// New builds a crawl worker.
func New(pool *pgxpool.Pool, concurrency int, pollInterval time.Duration) *Worker {
	if concurrency <= 0 {
		concurrency = 1
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}

	return &Worker{
		pool:         pool,
		queries:      sqlc.New(pool),
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

// runLoop repeatedly claims and processes queued crawls.
func (worker *Worker) runLoop(ctx context.Context, workerID int) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		claimedCrawl, err := worker.queries.ClaimNextQueuedCrawl(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if err := sleepOrCancel(ctx, worker.pollInterval); err != nil {
					return nil
				}
				continue
			}
			if isRunningCrawlConflict(err) {
				if err := sleepOrCancel(ctx, worker.pollInterval); err != nil {
					return nil
				}
				continue
			}

			return fmt.Errorf("claim queued crawl: %w", err)
		}

		log.Printf("worker %d claimed crawl: crawl_id=%s project_id=%s", workerID, claimedCrawl.ID.String(), claimedCrawl.ProjectID.String())
		if err := worker.runCrawl(ctx, claimedCrawl); err != nil {
			log.Printf("worker %d crawl failed: crawl_id=%s error=%v", workerID, claimedCrawl.ID.String(), err)
			continue
		}
		log.Printf("worker %d crawl completed: crawl_id=%s", workerID, claimedCrawl.ID.String())
	}
}

// runCrawl executes one claimed crawl.
func (worker *Worker) runCrawl(ctx context.Context, claimedCrawl sqlc.ClaimNextQueuedCrawlRow) error {
	crawlConfig, err := crawler.DefaultConfigFromBaseURL(claimedCrawl.BaseUrl)
	if err != nil {
		store := crawler.NewStore(worker.pool)
		if failErr := store.MarkCrawlFailed(ctx, claimedCrawl.ID, 0, 0, 0); failErr != nil {
			return fmt.Errorf("build crawler config: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("build crawler config: %w", err)
	}

	fetcher := crawler.NewFetcher(crawlConfig.FetchTimeout, crawlConfig.UserAgent)
	parser := crawler.NewParser()
	store := crawler.NewStore(worker.pool)
	runner := crawler.NewRunner(crawlConfig, crawler.DefaultWorkerCount, fetcher, parser).WithStore(store)

	if _, err := runner.RunAndPersist(ctx, claimedCrawl.ID, claimedCrawl.BaseUrl); err != nil {
		return fmt.Errorf("run crawl: %w", err)
	}

	return nil
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

// isRunningCrawlConflict reports whether the one-running-crawl-per-user index rejected a claim.
func isRunningCrawlConflict(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}

	return postgresError.Code == "23505"
}
