package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// crawlTimeout is the maximum wall-clock time a single runCrawl call may take.
// A hung upstream site would otherwise occupy a worker slot indefinitely,
// which is fatal when concurrency=1.
const crawlTimeout = 30 * time.Minute

// Worker claims queued crawls from Postgres and runs them.
type Worker struct {
	pool                 *pgxpool.Pool
	queries              *sqlc.Queries
	concurrency          int
	pollInterval         time.Duration
	crawlPageWorkerCount int
	renderer             *crawler.Renderer
	pageSpeedAPIKey      string
}

// New builds a crawl worker.
func New(pool *pgxpool.Pool, concurrency int, pollInterval time.Duration, crawlPageWorkerCount int, renderer *crawler.Renderer, pageSpeedAPIKey string) *Worker {
	if concurrency <= 0 {
		concurrency = 1
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	if crawlPageWorkerCount <= 0 {
		crawlPageWorkerCount = crawler.DefaultWorkerCount
	}

	return &Worker{
		pool:                 pool,
		queries:              sqlc.New(pool),
		concurrency:          concurrency,
		pollInterval:         pollInterval,
		crawlPageWorkerCount: crawlPageWorkerCount,
		renderer:             renderer,
		pageSpeedAPIKey:      pageSpeedAPIKey,
	}
}

// Run starts worker loops until the context is canceled.
// It uses errgroup so that a fatal error from any loop is propagated back to
// the caller, and so that context cancellation is propagated to all loops.
// Run blocks until all worker goroutines have fully returned.
//
// Graceful shutdown:
//   - claimCtx (the passed-in ctx) is canceled on signal; this stops workers
//     from claiming NEW crawls.
//   - In-flight runCrawl calls run under a separate drainCtx so they can
//     complete within crawlTimeout even after the signal arrives. The caller
//     is responsible for providing a drainCtx that outlives the signal context
//     (typically via context.WithTimeout on a fresh Background context).
//
// For callers that do not need drain semantics, passing a single ctx for both
// the claim loop and runCrawl is also valid — each runCrawl still gets its
// own per-crawl timeout via crawlTimeout.
func (worker *Worker) Run(ctx context.Context) error {
	return worker.RunWithDrain(ctx, ctx)
}

// RunWithDrain is like Run but accepts a separate drainCtx that is used for
// in-flight runCrawl calls after claimCtx is canceled. This enables graceful
// shutdown: workers stop claiming new jobs (claimCtx canceled) while still
// completing in-progress jobs (drainCtx not yet canceled).
func (worker *Worker) RunWithDrain(claimCtx, drainCtx context.Context) error {
	eg, groupCtx := errgroup.WithContext(claimCtx)
	for workerIndex := range worker.concurrency {
		id := workerIndex + 1
		eg.Go(func() error {
			return worker.runLoop(groupCtx, drainCtx, id)
		})
	}
	return eg.Wait()
}

// runLoop repeatedly claims and processes queued crawls.
// It returns nil when claimCtx is canceled (normal shutdown).
// claimCtx controls the claim-new-work loop; drainCtx is passed to runCrawl
// so that in-flight crawls can finish even after the signal has been received.
// Each job body is wrapped in a deferred recover so that a panic in runCrawl
// cannot permanently shrink the worker pool.
func (worker *Worker) runLoop(claimCtx, drainCtx context.Context, workerID int) error {
	for {
		select {
		case <-claimCtx.Done():
			return nil
		default:
		}

		claimedCrawl, err := worker.queries.ClaimNextQueuedCrawl(claimCtx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if err := sleepOrCancel(claimCtx, worker.pollInterval); err != nil {
					return nil
				}
				continue
			}
			if isRunningCrawlConflict(err) {
				if err := sleepOrCancel(claimCtx, worker.pollInterval); err != nil {
					return nil
				}
				continue
			}
			log.Printf("worker %d transient error claiming crawl: %v", workerID, err)
			if err := sleepOrCancel(claimCtx, worker.pollInterval); err != nil {
				return nil
			}
			continue
		}

		log.Printf("worker %d claimed crawl: crawl_id=%s project_id=%s", workerID, claimedCrawl.ID.String(), claimedCrawl.ProjectID.String())

		// Wrap the crawl execution in a closure with a deferred recover so
		// that panics in runCrawl are caught, logged with a stack trace, and
		// the loop continues rather than silently killing the goroutine.
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("worker %d panic in runCrawl crawl_id=%s: %v\n%s",
						workerID, claimedCrawl.ID.String(), r, debug.Stack())
				}
			}()

			// H-10: bound each individual crawl with a per-crawl timeout so
			// that a hung upstream site cannot occupy the slot indefinitely.
			// Use drainCtx as the parent so in-flight crawls can complete
			// after the claim loop has been stopped by a signal.
			crawlCtx, cancel := context.WithTimeout(drainCtx, crawlTimeout)
			defer cancel()

			if err := worker.runCrawl(crawlCtx, claimedCrawl); err != nil {
				log.Printf("worker %d crawl failed: crawl_id=%s error=%v", workerID, claimedCrawl.ID.String(), err)
				return
			}
			log.Printf("worker %d crawl completed: crawl_id=%s", workerID, claimedCrawl.ID.String())
		}()
	}
}

// runCrawl executes one claimed crawl, derives backend issues, and calculates crawl scores.
func (worker *Worker) runCrawl(ctx context.Context, claimedCrawl sqlc.ClaimNextQueuedCrawlRow) error {
	crawlConfig, err := crawler.ConfigFromBaseURLAndSnapshot(claimedCrawl.BaseUrl, claimedCrawl.ConfigSnapshot)
	if err != nil {
		store := crawler.NewStore(worker.pool)
		if failErr := store.MarkCrawlFailed(ctx, claimedCrawl.ID, 0, 0, 0); failErr != nil {
			return fmt.Errorf("build crawler config: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("build crawler config: %w", err)
	}

	fetcher := crawler.NewFetcher(crawlConfig.FetchTimeout, crawlConfig.UserAgent)
	parser := crawler.NewParser()
	crawlStore := crawler.NewStore(worker.pool)
	issueStore := issues.NewStore(worker.pool)
	runner := crawler.NewRunner(crawlConfig, worker.crawlPageWorkerCount, fetcher, parser).
		WithRenderer(worker.renderer).
		WithStore(crawlStore).
		WithDeferredFinalStatus()

	_, crawlRunSummary, err := runner.RunAndPersistWithSummary(ctx, claimedCrawl.ID, claimedCrawl.BaseUrl)
	if err != nil {
		return fmt.Errorf("run crawl: %w", err)
	}

	// M-15: log PSI start on the key-present path, and explicitly log the
	// skip on the no-key path so operators have observability in both cases.
	if worker.pageSpeedAPIKey != "" {
		log.Printf("starting google psi: crawl_id=%s url=%s strategy=mobile", claimedCrawl.ID.String(), claimedCrawl.BaseUrl)
	} else {
		log.Printf("skipping google psi: crawl_id=%s reason=no_key_configured", claimedCrawl.ID.String())
	}
	googlePSIResult, err := worker.enrichCrawlWithGooglePSI(ctx, claimedCrawl.ID, claimedCrawl.BaseUrl)
	if err != nil {
		log.Printf("google psi enrichment failed: crawl_id=%s error=%v", claimedCrawl.ID.String(), err)
	}

	derivedIssueCount, err := issueStore.DeriveIssues(ctx, claimedCrawl.ID)
	if err != nil {
		if failErr := crawlStore.MarkCrawlFailed(ctx, claimedCrawl.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); failErr != nil {
			return fmt.Errorf("derive issues: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("derive issues: %w", err)
	}

	googlePSIIssueCount, err := worker.persistGooglePSIIssues(ctx, claimedCrawl.ID, googlePSIResult)
	if err != nil {
		log.Printf("google psi issue persistence failed: crawl_id=%s error=%v", claimedCrawl.ID.String(), err)
	}
	psiInput := toSharedPSIScoreInput(googlePSIResult)
	crawlScores, err := issueStore.ScoreCrawl(ctx, claimedCrawl.ID, psiInput)
	if err != nil {
		if failErr := crawlStore.MarkCrawlFailed(ctx, claimedCrawl.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); failErr != nil {
			return fmt.Errorf("score crawl: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("score crawl: %w", err)
	}

	log.Printf("derived crawl issues: crawl_id=%s count=%d google_psi_count=%d", claimedCrawl.ID.String(), derivedIssueCount, googlePSIIssueCount)
	log.Printf("calculated crawl scores: crawl_id=%s seo=%d aeo=%d pagespeed=%d overall=%d", claimedCrawl.ID.String(), crawlScores.SEOScore, crawlScores.AEOScore, crawlScores.PageSpeedScore, crawlScores.OverallScore)
	if err := crawlStore.MarkCrawlCompleted(ctx, claimedCrawl.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); err != nil {
		return fmt.Errorf("mark crawl completed: %w", err)
	}

	return nil
}

// sleepOrCancel sleeps for a poll interval or returns when context is canceled.
func sleepOrCancel(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func toSharedPSIScoreInput(result *googlePSIStoredResult) *shared.GooglePSIScoreInput {
	if result == nil || !result.Mobile.Success || result.Mobile.PerformanceScore == nil {
		return nil
	}
	return &shared.GooglePSIScoreInput{MobilePerformanceScore: result.Mobile.PerformanceScore}

}

// isRunningCrawlConflict reports whether the one-running-crawl-per-user index rejected a claim.
func isRunningCrawlConflict(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}

	return postgresError.Code == "23505"
}
