package crawler

import (
	"bytes"
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
	HasLlmsTxt      *bool
}

// resultPersister defines the crawl status and persistence hooks used by the runner.
type resultPersister interface {
	MarkCrawlRunning(ctx context.Context, crawlID pgtype.UUID) error
	MarkCrawlCompleted(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int, hasLlmsTxt pgtype.Bool) error
	MarkCrawlFailed(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error
	PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error
	UpdateCrawlProgress(ctx context.Context, crawlID pgtype.UUID, urlsCrawled int, urlsDiscovered int) (bool, error)
}

// baselineReuser defines the incremental-crawl hooks used by the runner when a
// conditional-request baseline is attached. It is deliberately separate from
// resultPersister so existing implementations of that interface (including test
// fakes) keep compiling without an incremental code path.
type baselineReuser interface {
	PersistReusedResult(ctx context.Context, crawlID pgtype.UUID, baseline *Baseline, result CrawlResult) error
}

// Runner coordinates an in-memory BFS crawl over the worker pool.
type Runner struct {
	config           CrawlerConfig
	workerCount      int
	fetcher          *Fetcher
	parser           *Parser
	renderer         htmlRenderer
	store            resultPersister
	baseline         *Baseline
	baselineStore    baselineReuser
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

// WithBaseline enables incremental crawling against a previous completed crawl.
// Passing a nil baseline (or nil store) leaves the crawl fully unconditional.
func (runner *Runner) WithBaseline(baseline *Baseline, baselineStore baselineReuser) *Runner {
	if baseline == nil || baseline.Len() == 0 || baselineStore == nil {
		return runner
	}
	runner.baseline = baseline
	runner.baselineStore = baselineStore
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
	// The root /llms.txt is reserved for the explicit probe below: it is never
	// scheduled as a crawl page, but sitemap entries and discovered links must not
	// re-add it either, so it is marked seen before either runs.
	llmsURL := normalizedRootURL.Scheme + "://" + normalizedRootURL.Host + "/llms.txt"
	seenURLs[llmsURL] = struct{}{}
	processedPageURLs := make(map[string]struct{})

	scheduledPages := 1
	activeJobs := 0
	urlsCrawled := 0
	urlsRendered := 0
	urlsReused := 0
	urlsSkippedNon2xx := 0
	urlsSoftNotFound := 0
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

	var hasLlmsTxt *bool
	if runner.fetcher != nil {
		llmsResult := runner.fetcher.Fetch(runContext, llmsURL)
		has := llmsResult.FetchError == nil && llmsResult.StatusCode == 200 && len(bytes.TrimSpace(llmsResult.Body)) > 0
		hasLlmsTxt = &has
	}

	// One probe for a URL that cannot exist. If the origin answers 2xx instead of
	// 404, every page matching this fingerprint is a soft 404. Nil means the
	// origin returns proper 404s, or the probe could not be evaluated.
	softNotFoundFingerprint := DetectSoftNotFound(runContext, runner.fetcher, runner.parser, normalizedRootURL.String())

	var crawlResults []CrawlResult
	crawlStartedAt := time.Now()
	var lastProgressWrite time.Time
	// The result loop is single-threaded, so every millisecond spent persisting
	// here is a millisecond no page worker can be handed a job. Accumulated so the
	// throughput line shows whether dispatch is starved by persistence.
	var persistElapsed time.Duration

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

	// enqueueInternalTarget adds one internal link target to the frontier,
	// honoring the page cap, host scope, and dedupe. Shared by the parsed-link
	// path and the reused-page path, which supply targets in different shapes.
	enqueueInternalTarget := func(targetURL string, childDepth int) {
		if runner.config.MaxPages > 0 && scheduledPages >= runner.config.MaxPages {
			return
		}

		normalizedLinkURL, normalizeErr := NormalizeURL(targetURL, nil)
		if normalizeErr != nil {
			return
		}
		if runner.config.AllowedHost != "" && !IsAllowedHost(runner.config.AllowedHost, normalizedLinkURL.Hostname()) {
			return
		}
		if _, alreadySeen := seenURLs[normalizedLinkURL.String()]; alreadySeen {
			return
		}

		seenURLs[normalizedLinkURL.String()] = struct{}{}
		scheduledPages++
		pendingQueue = append(pendingQueue, CrawlJob{URL: normalizedLinkURL.String(), Depth: childDepth})
	}

	for len(pendingQueue) > 0 || activeJobs > 0 {
		var nextJob CrawlJob
		var jobsChannel chan<- CrawlJob
		if len(pendingQueue) > 0 {
			nextJob = pendingQueue[0]
			// Single point where validators are attached, so every frontier source
			// (root, sitemap, discovered links, reused links) gets them uniformly.
			if baselinePage, ok := runner.baseline.lookup(nextJob.URL); ok {
				nextJob.ETag = baselinePage.etag
				nextJob.LastModified = baselinePage.lastModified
			}
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
			if shouldPersist {
				// The persist path (worker.go) discards the returned slice and
				// only reads the final CrawlRunSummary, so retain a lightweight
				// copy instead of every page's full HTML body and visible text
				// (avoids ~100MB+ retained per large crawl). PersistResult below
				// still persists the untrimmed result to the DB.
				lightResult := result
				lightResult.Fetch.Body = nil
				lightResult.ParsedPage = nil
				crawlResults = append(crawlResults, lightResult)
			} else {
				crawlResults = append(crawlResults, result)
			}

			// A 304 is checked before the non-2xx gate below, which would otherwise
			// discard it as an error page. The origin confirmed the body is
			// unchanged, so the baseline crawl's facts and outbound links are
			// copied forward and the parse/render is skipped entirely.
			// A 304 without a baseline to copy from means the origin answered
			// conditionally when we sent no validator. Nothing can be reused, and
			// the page has no facts, so treat it as a skip rather than dereferencing
			// a nil baseline.
			if result.NotModified && (runner.baseline == nil || runner.baselineStore == nil) {
				urlsSkippedNon2xx++
				log.Printf("unsolicited 304 with no crawl baseline, skipping: url=%q", result.Job.URL)
				maybeWriteProgress()
				continue
			}

			if result.NotModified {
				normalizedReusedURL, normalizeErr := NormalizeURL(crawlPageURL(result), nil)
				if normalizeErr == nil {
					if _, alreadyProcessed := processedPageURLs[normalizedReusedURL.String()]; alreadyProcessed {
						maybeWriteProgress()
						continue
					}
					processedPageURLs[normalizedReusedURL.String()] = struct{}{}
					seenURLs[normalizedReusedURL.String()] = struct{}{}
				}

				urlsCrawled++
				urlsReused++
				if result.Job.Depth > maxDepthReached {
					maxDepthReached = result.Job.Depth
				}

				if shouldPersist {
					persistStartedAt := time.Now()
					err := runner.baselineStore.PersistReusedResult(runContext, crawlID, runner.baseline, result)
					persistElapsed += time.Since(persistStartedAt)
					if err != nil {
						cancelRun()
						summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached}
						finalCtx := context.WithoutCancel(ctx)
						if failErr := runner.store.MarkCrawlFailed(finalCtx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached); failErr != nil {
							return crawlResults, summary, fmt.Errorf("persist reused crawl result for %q: %w (also failed to mark crawl failed: %v)", result.Job.URL, err, failErr)
						}
						return crawlResults, summary, fmt.Errorf("persist reused crawl result for %q: %w", result.Job.URL, err)
					}
				}

				// Without this the frontier would stop expanding at every reused
				// page, since the skipped parse produced no outbound links. Served
				// from the preloaded baseline graph — no DB round trip here.
				if result.Job.Depth < runner.config.MaxDepth {
					for _, reusedTarget := range runner.baseline.internalTargets(result.Job.URL) {
						enqueueInternalTarget(reusedTarget, result.Job.Depth+1)
					}
				}

				log.Printf("crawl progress: crawled=%d/%d in_flight=%d rendered=%d reused=%d non2xx=%d soft404=%d (root=%s)",
					urlsCrawled, scheduledPages, activeJobs, urlsRendered, urlsReused, urlsSkippedNon2xx, urlsSoftNotFound, rootURL)
				maybeWriteProgress()
				continue
			}

			// Non-2xx responses are persisted so a broken page is visible in the
			// site graph and can be attributed to the pages linking to it. They are
			// still not parsed or rendered (see ProcessJob) and still do not expand
			// the frontier: a 404 has no trustworthy links to follow.
			isNon2xx := result.Fetch.StatusCode != 0 && (result.Fetch.StatusCode < 200 || result.Fetch.StatusCode > 299)
			if isNon2xx {
				urlsSkippedNon2xx++
			} else if softNotFoundFingerprint.Matches(result.ParsedPage) || LooksLikeSoftNotFound(result.ParsedPage) {
				// A soft 404 answered 2xx, so it reached here as a normal page. Mark
				// it and stop it expanding the frontier for the same reason a hard
				// 404 does not.
				result.SoftNotFound = true
				urlsSoftNotFound++
				log.Printf("soft 404 detected: url=%q", crawlPageURL(result))
			}

			urlsCrawled++
			if result.JavascriptRendered {
				urlsRendered++
			}
			if result.Job.Depth > maxDepthReached {
				maxDepthReached = result.Job.Depth
			}

			log.Printf("crawl progress: crawled=%d/%d in_flight=%d rendered=%d non2xx=%d soft404=%d (root=%s)",
				urlsCrawled, scheduledPages, activeJobs, urlsRendered, urlsSkippedNon2xx, urlsSoftNotFound, rootURL)
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
				persistStartedAt := time.Now()
				err := runner.store.PersistResult(runContext, crawlID, normalizedRootURL.String(), result)
				persistElapsed += time.Since(persistStartedAt)
				if err != nil {
					cancelRun()
					summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached}
					// Use a ctx detached from cancellation so this terminal status
					// write still lands even if the parent ctx is already done
					// (e.g. process shutdown), matching worker.go's post-crawl writes.
					finalCtx := context.WithoutCancel(ctx)
					if failErr := runner.store.MarkCrawlFailed(finalCtx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached); failErr != nil {
						return crawlResults, summary, fmt.Errorf("persist crawl result for %q: %w (also failed to mark crawl failed: %v)", result.Job.URL, err, failErr)
					}
					return crawlResults, summary, fmt.Errorf("persist crawl result for %q: %w", result.Job.URL, err)
				}
			}

			// A soft 404 has a parsed body, so unlike a hard 404 it would otherwise
			// expand the frontier — usually into the site's own nav, which is
			// already covered, or into more nonexistent URLs.
			if !isDuplicateProcessedPage && !result.SoftNotFound && result.ProcessErr == nil && result.ParsedPage != nil && result.Job.Depth < runner.config.MaxDepth {
				for _, parsedLink := range result.ParsedPage.Links {
					if !parsedLink.IsInternal {
						continue
					}

					enqueueInternalTarget(parsedLink.TargetURL, result.Job.Depth+1)
				}
			}
		case <-runContext.Done():
			summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached}
			if shouldPersist {
				// Use a ctx detached from cancellation so this terminal status
				// write still lands even when the parent ctx is already
				// cancelled (e.g. process shutdown); otherwise the row is
				// stuck in 'running' forever.
				finalCtx := context.WithoutCancel(ctx)
				if failErr := runner.store.MarkCrawlFailed(finalCtx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached); failErr != nil {
					return crawlResults, summary, fmt.Errorf("crawl canceled: %w (also failed to mark crawl failed: %v)", runContext.Err(), failErr)
				}
			}
			return crawlResults, summary, fmt.Errorf("crawl canceled: %w", runContext.Err())
		}
	}

	close(jobs)
	for range results {
	}

	summary := CrawlRunSummary{URLsDiscovered: scheduledPages, URLsCrawled: urlsCrawled, MaxDepthReached: maxDepthReached, HasLlmsTxt: hasLlmsTxt}
	elapsed := time.Since(crawlStartedAt)
	pagesPerSec := 0.0
	if elapsed.Seconds() > 0 {
		pagesPerSec = float64(urlsCrawled) / elapsed.Seconds()
	}
	log.Printf("crawl throughput: crawled=%d rendered=%d reused_304=%d baseline_pages=%d non2xx=%d soft404=%d workers=%d elapsed=%s persist_serial=%s pages_per_sec=%.2f (root=%s)",
		urlsCrawled, urlsRendered, urlsReused, runner.baseline.Len(), urlsSkippedNon2xx, urlsSoftNotFound, runner.workerCount, elapsed.Round(time.Millisecond), persistElapsed.Round(time.Millisecond), pagesPerSec, rootURL)

	if shouldPersist && !runner.deferFinalStatus {
		if err := runner.store.MarkCrawlCompleted(ctx, crawlID, summary.URLsDiscovered, summary.URLsCrawled, summary.MaxDepthReached, hasLlmsTxtToPGBool(summary.HasLlmsTxt)); err != nil {
			return crawlResults, summary, fmt.Errorf("mark crawl completed: %w", err)
		}
	}

	return crawlResults, summary, nil
}

func hasLlmsTxtToPGBool(hasLlmsTxt *bool) pgtype.Bool {
	if hasLlmsTxt == nil {
		return pgtype.Bool{Valid: false}
	}
	return pgtype.Bool{Bool: *hasLlmsTxt, Valid: true}
}
