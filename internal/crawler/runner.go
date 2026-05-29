package crawler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// Runner coordinates an in-memory BFS crawl over the worker pool.
type resultPersister interface {
	MarkCrawlRunning(ctx context.Context, crawlID pgtype.UUID) error
	MarkCrawlCompleted(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error
	MarkCrawlFailed(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error
	PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error
}

// Runner coordinates an in-memory BFS crawl over the worker pool.
type Runner struct {
	config      CrawlerConfig
	workerCount int
	fetcher     *Fetcher
	parser      *Parser
	store       resultPersister
}

// NewRunner builds a crawl runner from the provided dependencies.
func NewRunner(config CrawlerConfig, workerCount int, fetcher *Fetcher, parser *Parser) *Runner {
	return &Runner{
		config:      config,
		workerCount: workerCount,
		fetcher:     fetcher,
		parser:      parser,
	}
}

// WithStore attaches a persistence store to the runner.
func (runner *Runner) WithStore(store resultPersister) *Runner {
	runner.store = store
	return runner
}

// Run crawls one root URL in memory and returns the processed crawl results.
func (runner *Runner) Run(ctx context.Context, rootURL string) ([]CrawlResult, error) {
	return runner.run(ctx, pgtype.UUID{}, rootURL, false)
}

// RunAndPersist crawls one root URL and persists each processed result.
func (runner *Runner) RunAndPersist(ctx context.Context, crawlID pgtype.UUID, rootURL string) ([]CrawlResult, error) {
	if runner.store == nil {
		return nil, fmt.Errorf("runner store is not configured")
	}

	return runner.run(ctx, crawlID, rootURL, true)
}

func (runner *Runner) run(ctx context.Context, crawlID pgtype.UUID, rootURL string, shouldPersist bool) ([]CrawlResult, error) {
	if runner.workerCount <= 0 {
		return nil, fmt.Errorf("worker count must be greater than zero")
	}

	if runner.config.MaxPages <= 0 {
		return nil, fmt.Errorf("max pages must be greater than zero")
	}

	normalizedRootURL, err := NormalizeURL(rootURL, nil)
	if err != nil {
		return nil, fmt.Errorf("normalize root url: %w", err)
	}

	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	jobs := make(chan CrawlJob, runner.workerCount)
	results := StartWorkerPool(runContext, runner.workerCount, runner.fetcher, runner.parser, jobs)

	if shouldPersist {
		if err := runner.store.MarkCrawlRunning(runContext, crawlID); err != nil {
			return nil, fmt.Errorf("mark crawl running: %w", err)
		}
	}

	seenURLs := map[string]struct{}{
		normalizedRootURL.String(): {},
	}

	scheduledPages := 1
	pendingJobs := 1
	urlsCrawled := 0
	maxDepthReached := 0
	jobs <- CrawlJob{URL: normalizedRootURL.String(), Depth: 0}

	var crawlResults []CrawlResult

	for result := range results {
		crawlResults = append(crawlResults, result)
		pendingJobs--
		urlsCrawled++
		if result.Job.Depth > maxDepthReached {
			maxDepthReached = result.Job.Depth
		}

		if shouldPersist {
			if err := runner.store.PersistResult(runContext, crawlID, normalizedRootURL.String(), result); err != nil {
				cancelRun()
				close(jobs)
				if failErr := runner.store.MarkCrawlFailed(ctx, crawlID, scheduledPages, urlsCrawled, maxDepthReached); failErr != nil {
					return crawlResults, fmt.Errorf("persist crawl result for %q: %w (also failed to mark crawl failed: %v)", result.Job.URL, err, failErr)
				}
				return crawlResults, fmt.Errorf("persist crawl result for %q: %w", result.Job.URL, err)
			}
		}

		if result.ProcessErr == nil && result.ParsedPage != nil && result.Job.Depth < runner.config.MaxDepth {
			for _, parsedLink := range result.ParsedPage.Links {
				if !parsedLink.IsInternal {
					continue
				}

				if scheduledPages >= runner.config.MaxPages {
					break
				}

				normalizedLinkURL, normalizeErr := NormalizeURL(parsedLink.TargetURL, nil)
				if normalizeErr != nil {
					continue
				}

				if runner.config.AllowedHost != "" && normalizedLinkURL.Host != runner.config.AllowedHost {
					continue
				}

				if _, alreadySeen := seenURLs[normalizedLinkURL.String()]; alreadySeen {
					continue
				}

				seenURLs[normalizedLinkURL.String()] = struct{}{}
				scheduledPages++
				pendingJobs++
				jobs <- CrawlJob{URL: normalizedLinkURL.String(), Depth: result.Job.Depth + 1}
			}
		}

		if pendingJobs == 0 {
			close(jobs)
		}
	}

	if shouldPersist {
		if err := runner.store.MarkCrawlCompleted(ctx, crawlID, scheduledPages, urlsCrawled, maxDepthReached); err != nil {
			return crawlResults, fmt.Errorf("mark crawl completed: %w", err)
		}
	}

	return crawlResults, nil
}
