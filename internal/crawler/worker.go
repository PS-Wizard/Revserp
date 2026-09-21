package crawler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// ProcessJob fetches and parses one crawl job.
func ProcessJob(ctx context.Context, fetcher *Fetcher, parser *Parser, renderer htmlRenderer, job CrawlJob) CrawlResult {
	fetchResult := fetcher.FetchConditional(ctx, job.URL, job.ETag, job.LastModified)
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

	// A 304 must be handled before the non-2xx skip below: the page is unchanged,
	// not missing. Returning here skips the parse and the JS render, and the
	// caller copies the baseline crawl's facts forward instead.
	if fetchResult.NotModified {
		crawlResult.NotModified = true
		return crawlResult
	}

	// Skip processing for non-2xx responses (e.g. 429 rate-limit / challenge pages).
	// Rendering and parsing error pages wastes JS render quota and pollutes stored content.
	if fetchResult.StatusCode != 0 && (fetchResult.StatusCode < 200 || fetchResult.StatusCode > 299) {
		log.Printf("skipping non-2xx page: url=%q status=%d", job.URL, fetchResult.StatusCode)
		return crawlResult
	}

	if !strings.Contains(strings.ToLower(fetchResult.ContentType), "text/html") {
		return crawlResult
	}

	parsedPage, err := parser.ParseHTML(fetchResult.FinalURL, fetchResult.ContentType, fetchResult.Body)
	if err != nil {
		crawlResult.ProcessErr = fmt.Errorf("parse job %q: %w", job.URL, err)
		return crawlResult
	}

	renderDecision := NeedsJSRender(fetchResult, &parsedPage)
	crawlResult.WouldHaveRendered = renderDecision.NeedsRender

	if renderDecision.NeedsRender {
		if renderer == nil {
			log.Printf("js fallback skipped (disabled): url=%q reasons=%q", job.URL, renderDecision.Reasons)
		} else {
			log.Printf("js fallback triggered: url=%q reasons=%q", job.URL, renderDecision.Reasons)
			renderStarted := time.Now()
			renderedFetchResult, renderErr := renderer.RenderHTML(ctx, job.URL)
			renderDuration := time.Since(renderStarted).Round(time.Millisecond)
			if renderErr != nil {
				log.Printf("js fallback failed: url=%q duration=%s error=%v", job.URL, renderDuration, renderErr)
			} else {
				renderedParsedPage, parseRenderedErr := parser.ParseHTML(renderedFetchResult.FinalURL, renderedFetchResult.ContentType, renderedFetchResult.Body)
				switch {
				case parseRenderedErr != nil:
					log.Printf("js fallback parse failed: url=%q error=%v", job.URL, parseRenderedErr)
				case shouldPreferRenderedPage(parsedPage, renderedParsedPage):
					log.Printf("js fallback applied: url=%q duration=%s", job.URL, renderDuration)
					crawlResult.Fetch = renderedFetchResult
					crawlResult.ParsedPage = &renderedParsedPage
					crawlResult.JavascriptRendered = true
					return crawlResult
				default:
					log.Printf("js fallback discarded: url=%q duration=%s", job.URL, renderDuration)
				}
			}
		}
	}

	crawlResult.ParsedPage = &parsedPage
	return crawlResult
}
