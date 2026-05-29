package crawler

import (
	"context"
	"fmt"
	"strings"
)

// CrawlResult holds the processed outcome of one crawl job.
type CrawlResult struct {
	Job        CrawlJob
	Fetch      FetchResult
	ParsedPage *ParsedPage
	ProcessErr error
}

// ProcessJob fetches and parses one crawl job.
func ProcessJob(ctx context.Context, fetcher *Fetcher, parser *Parser, job CrawlJob) CrawlResult {
	fetchResult := fetcher.Fetch(ctx, job.URL)
	if fetchResult.FetchError != nil {
		return CrawlResult{
			Job:        job,
			Fetch:      fetchResult,
			ProcessErr: fmt.Errorf("fetch job %q: %w", job.URL, fetchResult.FetchError),
		}
	}

	crawlResult := CrawlResult{
		Job:   job,
		Fetch: fetchResult,
	}

	if !strings.Contains(strings.ToLower(fetchResult.ContentType), "text/html") {
		return crawlResult
	}

	parsedPage, err := parser.ParseHTML(fetchResult.FinalURL, fetchResult.ContentType, fetchResult.Body)
	if err != nil {
		crawlResult.ProcessErr = fmt.Errorf("parse job %q: %w", job.URL, err)
		return crawlResult
	}

	crawlResult.ParsedPage = &parsedPage
	return crawlResult
}
