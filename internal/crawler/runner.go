package crawler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// CrawlRunSummary holds final crawl counters for one run.
type CrawlRunSummary struct {
	URLsDiscovered  int
	URLsCrawled     int
	MaxDepthReached int
}

// resultPersister defines the crawl status and persistence hooks used by the runner.
type resultPersister interface {
	MarkCrawlRunning(ctx context.Context, crawlID pgtype.UUID) error
	MarkCrawlCompleted(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error
	MarkCrawlFailed(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error
	PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error
	UpdateCrawlProgress(ctx context.Context, crawlID pgtype.UUID, urlsCrawled int, urlsDiscovered int) (bool, error)
}

// Runner coordinates an in-memory BFS crawl over the worker pool.
type Runner struct {
	config           CrawlerConfig
	workerCount      int
	fetcher          *Fetcher
	parser           *Parser
	renderer         htmlRenderer
	store            resultPersister
	deferFinalStatus bool
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

// WithRenderer attaches a JavaScript renderer fallback to the runner.
func (runner *Runner) WithRenderer(renderer htmlRenderer) *Runner {
	runner.renderer = renderer
	return runner
}

// WithDeferredFinalStatus leaves crawl completion for a later step after persistence succeeds.
func (runner *Runner) WithDeferredFinalStatus() *Runner {
	runner.deferFinalStatus = true
	return runner
}

// Run crawls one root URL in memory and returns the processed crawl results.
func (runner *Runner) Run(ctx context.Context, rootURL string) ([]CrawlResult, error) {
	crawlResults, _, err := runner.run(ctx, pgtype.UUID{}, rootURL, false)
	return crawlResults, err
}

// RunAndPersist crawls one root URL and persists each processed result.
func (runner *Runner) RunAndPersist(ctx context.Context, crawlID pgtype.UUID, rootURL string) ([]CrawlResult, error) {
	crawlResults, _, err := runner.RunAndPersistWithSummary(ctx, crawlID, rootURL)
	return crawlResults, err
}

// RunAndPersistWithSummary crawls one root URL, persists each result, and returns final crawl counters.
func (runner *Runner) RunAndPersistWithSummary(ctx context.Context, crawlID pgtype.UUID, rootURL string) ([]CrawlResult, CrawlRunSummary, error) {
	if runner.store == nil {
		return nil, CrawlRunSummary{}, fmt.Errorf("runner store is not configured")
	}

	return runner.run(ctx, crawlID, rootURL, true)
}

// run executes the crawl loop and optionally persists results.
func (runner *Runner) run(ctx context.Context, crawlID pgtype.UUID, rootURL string, shouldPersist bool) ([]CrawlResult, CrawlRunSummary, error) {
	if runner.workerCount <= 0 {
		return nil, CrawlRunSummary{}, fmt.Errorf("worker count must be greater than zero")
	}
	if runner.config.MaxPages < 0 {
		return nil, CrawlRunSummary{}, fmt.Errorf("max pages must be greater than or equal to zero")
	}

	normalizedRootURL, err := NormalizeURL(rootURL, nil)
	if err != nil {
		return nil, CrawlRunSummary{}, fmt.Errorf("normalize root url: %w", err)
	}

	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	jobs := make(chan CrawlJob, runner.workerCount)
	results := StartWorkerPool(runContext, runner.workerCount, runner.fetcher, runner.parser, runner.renderer, jobs, runner.config.RequestDelay, runner.config.RequestJitter)

	if shouldPersist {
		if err := runner.store.MarkCrawlRunning(runContext, crawlID); err != nil {
			return nil, CrawlRunSummary{}, fmt.Errorf("mark crawl running: %w", err)
		}
	}

	seenURLs := map[string]struct{}{
		normalizedRootURL.String(): {},
	}
	processedPageURLs := make(map[string]struct{})

	scheduledPages := 1
	activeJobs := 0
	urlsCrawled := 0
	urlsRendered := 0
	urlsSkippedNon2xx := 0
	maxDepthReached := 0
	pendingQueue := []CrawlJob{{URL: normalizedRootURL.String(), Depth: 0}}

	// Seed the frontier from the site's sitemap when available. This gives complete,
	// flat URL coverage without rendering pages just to discover links; plain
	// link-following BFS still runs afterward to catch anything the sitemap omits.
	sitemapURLs := DiscoverSitemapURLs(runContext, runner.fetcher, normalizedRootURL, runner.config.AllowedHost, runner.config.MaxPages)
	sitemapSeeded := 0
	for _, sitemapURL := range sitemapURLs {
		if runner.config.MaxPages > 0 && scheduledPages >= runner.config.MaxPages {
			break
		}
		if _, alreadySeen := seenURLs[sitemapURL]; alreadySeen {
			continue
		}
		seenURLs[sitemapURL] = struct{}{}
		scheduledPages++
		sitemapSeeded++
		pendingQueue = append(pendingQueue, CrawlJob{URL: sitemapURL, Depth: 0})
	}
	if sitemapSeeded > 0 {
		log.Printf("sitemap discovery: seeded %d urls (root=%q)", sitemapSeeded, normalizedRootURL.String())
	}

	var crawlResults []CrawlResult
	crawlStartedAt := time.Now()
	var lastProgressWrite time.Time

	// Throttled progress write that doubles as a cancellation probe: the UPDATE
	// is gated on status = 'running', so if the API marked this crawl cancelled
	// in another process, we observe zero rows affected and stop the run.
	maybeWriteProgress := func() {
		if !shouldPersist || time.Since(lastProgressWrite) < 2*time.Second {
			return
		}
		lastProgressWrite = time.Now()
		stillRunning, progressErr := runner.store.UpdateCrawlProgress(runContext, crawlID, urlsCrawled, scheduledPages)
		if progressErr != nil {
			log.Printf("crawl progress write failed: %v", progressErr)
			return
		}
		if !stillRunning {
			log.Printf("crawl no longer running (cancelled), stopping: crawl_id=%s", crawlID.String())
			cancelRun()
		}
	}

	for len(pendingQueue) > 0 || activeJobs > 0 {
		var nextJob CrawlJob
		var jobsChannel chan<- CrawlJob
		if len(pendingQueue) > 0 {
			nextJob = pendingQueue[0]
			jobsChannel = jobs
		}

		select {
		case jobsChannel <- nextJob:
			pendingQueue = pendingQueue[1:]
			activeJobs++
		case result, ok := <-results:
			if !ok {
				return crawlResults, CrawlRunSummary{}, fmt.Errorf("worker pool closed unexpectedly")
			}

			activeJobs--
			crawlResults = append(crawlResults, result)

			// Non-2xx responses (rate limits, challenge pages, server errors) are
			// counted but not persisted or used for link discovery.
			isNon2xx := result.Fetch.StatusCode != 0 && (result.Fetch.StatusCode < 200 || result.Fetch.StatusCode > 299)
			if isNon2xx {
				urlsSkippedNon2xx++
				log.Printf("crawl progress: crawled=%d/%d in_flight=%d rendered=%d skipped=%d (root=%s)",
					urlsCrawled, scheduledPages, activeJobs, urlsRendered, urlsSkippedNon2xx, rootURL)
				maybeWriteProgress()
				continue
			}

			urlsCrawled++
			if result.JavascriptRendered {
				urlsRendered++
			}
			if result.Job.Depth > maxDepthReached {
				maxDepthReached = result.Job.Depth
			}

			log.Printf("crawl progress: crawled=%d/%d in_flight=%d rendered=%d skipped=%d (root=%s)",
				urlsCrawled, scheduledPages, activeJobs, urlsRendered, urlsSkippedNon2xx, rootURL)
			maybeWriteProgress()

			isDuplicateProcessedPage := false
			normalizedResultURL, normalizeErr := NormalizeURL(crawlPageURL(result), nil)
			if normalizeErr == nil {
				if _, alreadyProcessed := processedPageURLs[normalizedResultURL.String()]; alreadyProcessed {
					isDuplicateProcessedPage = true
				} else {
					processedPageURLs[normalizedResultURL.String()] = struct{}{}
					seenURLs[normalizedResultURL.String()] = struct{}{}
				}
			}

			if !isDuplicateProcessedPage && shouldPersist {
				if err := runner.store.PersistResult(runContext, crawlID, normalizedRootURL.String(), result); err != nil {
					cancelRun()
					summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached}
					if failErr := runner.store.MarkCrawlFailed(ctx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached); failErr != nil {
						return crawlResults, summary, fmt.Errorf("persist crawl result for %q: %w (also failed to mark crawl failed: %v)", result.Job.URL, err, failErr)
					}
					return crawlResults, summary, fmt.Errorf("persist crawl result for %q: %w", result.Job.URL, err)
				}
			}

			if !isDuplicateProcessedPage && result.ProcessErr == nil && result.ParsedPage != nil && result.Job.Depth < runner.config.MaxDepth {
				for _, parsedLink := range result.ParsedPage.Links {
					if !parsedLink.IsInternal {
						continue
					}
					if runner.config.MaxPages > 0 && scheduledPages >= runner.config.MaxPages {
						break
					}

					normalizedLinkURL, normalizeErr := NormalizeURL(parsedLink.TargetURL, nil)
					if normalizeErr != nil {
						continue
					}
					if runner.config.AllowedHost != "" && !IsAllowedHost(runner.config.AllowedHost, normalizedLinkURL.Hostname()) {
						continue
					}
					if _, alreadySeen := seenURLs[normalizedLinkURL.String()]; alreadySeen {
						continue
					}

					seenURLs[normalizedLinkURL.String()] = struct{}{}
					scheduledPages++
					pendingQueue = append(pendingQueue, CrawlJob{URL: normalizedLinkURL.String(), Depth: result.Job.Depth + 1})
				}
			}
		case <-runContext.Done():
			summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached}
			if shouldPersist {
				if failErr := runner.store.MarkCrawlFailed(ctx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached); failErr != nil {
					return crawlResults, summary, fmt.Errorf("crawl canceled: %w (also failed to mark crawl failed: %v)", runContext.Err(), failErr)
				}
			}
			return crawlResults, summary, fmt.Errorf("crawl canceled: %w", runContext.Err())
		}
	}

	close(jobs)
	for range results {
	}

	summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached}
	elapsed := time.Since(crawlStartedAt)
	pagesPerSec := 0.0
	if elapsed.Seconds() > 0 {
		pagesPerSec = float64(urlsCrawled) / elapsed.Seconds()
	}
	log.Printf("crawl throughput: crawled=%d rendered=%d skipped_non2xx=%d workers=%d elapsed=%s pages_per_sec=%.2f (root=%s)",
		urlsCrawled, urlsRendered, urlsSkippedNon2xx, runner.workerCount, elapsed.Round(time.Millisecond), pagesPerSec, rootURL)

	if shouldPersist && !runner.deferFinalStatus {
		if err := runner.store.MarkCrawlCompleted(ctx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached); err != nil {
			return crawlResults, summary, fmt.Errorf("mark crawl completed: %w", err)
		}
	}

	return crawlResults, summary, nil
}
