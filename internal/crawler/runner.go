package crawler

import (
	"context"
	"fmt"
)

// Runner coordinates an in-memory BFS crawl over the worker pool.
type Runner struct {
	config      CrawlerConfig
	workerCount int
	fetcher     *Fetcher
	parser      *Parser
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

// Run crawls one root URL in memory and returns the processed crawl results.
func (runner *Runner) Run(ctx context.Context, rootURL string) ([]CrawlResult, error) {
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

	jobs := make(chan CrawlJob, runner.workerCount)
	results := StartWorkerPool(ctx, runner.workerCount, runner.fetcher, runner.parser, jobs)

	seenURLs := map[string]struct{}{
		normalizedRootURL.String(): {},
	}

	scheduledPages := 1
	pendingJobs := 1
	jobs <- CrawlJob{URL: normalizedRootURL.String(), Depth: 0}

	var crawlResults []CrawlResult

	for result := range results {
		crawlResults = append(crawlResults, result)
		pendingJobs--

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

	return crawlResults, nil
}
