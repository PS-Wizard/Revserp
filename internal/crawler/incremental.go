package crawler

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// baselinePage holds one previous-crawl page's cache validators plus the exact
// URL string it was stored under, which the copy-forward statements match on.
type baselinePage struct {
	storedURL    string
	etag         string
	lastModified string
}

// Baseline holds the previous completed crawl's per-page cache validators, so
// this crawl can ask each origin whether a page changed instead of downloading
// and re-parsing it unconditionally.
//
// Pages are keyed by normalized URL so a job URL matches despite trailing-slash
// or case differences, while storedURL preserves the exact value the copy
// statements need.
type Baseline struct {
	CrawlID pgtype.UUID
	pages   map[string]baselinePage
	// internalTargetsBySource is the baseline's internal link graph, preloaded in
	// one query. An unchanged body has unchanged outlinks, so this stands in for
	// the parse a 304 lets us skip. Preloaded rather than queried per reused page
	// because the runner's result loop is single-threaded: every round trip it
	// makes there is time all the page workers spend idle.
	internalTargetsBySource map[string][]string
}

// Len reports how many baseline pages carry a usable validator.
func (baseline *Baseline) Len() int {
	if baseline == nil {
		return 0
	}

	return len(baseline.pages)
}

// lookup returns the baseline entry for one job URL, if the previous crawl
// stored a validator for it.
func (baseline *Baseline) lookup(jobURL string) (baselinePage, bool) {
	if baseline == nil || len(baseline.pages) == 0 {
		return baselinePage{}, false
	}

	normalizedURL, err := NormalizeURL(jobURL, nil)
	if err != nil {
		return baselinePage{}, false
	}

	page, ok := baseline.pages[normalizedURL.String()]

	return page, ok
}

// LoadBaseline builds the conditional-request baseline from the most recent
// completed crawl of a project. It returns (nil, nil) when there is no usable
// baseline — no previous completed crawl, or none of its pages carried a
// validator — which callers treat as "crawl everything unconditionally".
func (store *Store) LoadBaseline(ctx context.Context, projectID pgtype.UUID, currentCrawlID pgtype.UUID) (*Baseline, error) {
	baselineCrawlID, err := store.queries.GetLatestCompletedCrawlIDForProject(ctx, sqlc.GetLatestCompletedCrawlIDForProjectParams{
		ProjectID:      projectID,
		ExcludeCrawlID: currentCrawlID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("get latest completed crawl for project: %w", err)
	}

	validatorRows, err := store.queries.ListPageValidatorsForCrawl(ctx, baselineCrawlID)
	if err != nil {
		return nil, fmt.Errorf("list page validators for crawl: %w", err)
	}

	pages := make(map[string]baselinePage, len(validatorRows))
	for _, validatorRow := range validatorRows {
		normalizedURL, normalizeErr := NormalizeURL(validatorRow.Url, nil)
		if normalizeErr != nil {
			continue
		}

		pages[normalizedURL.String()] = baselinePage{
			storedURL:    validatorRow.Url,
			etag:         validatorRow.Etag.String,
			lastModified: validatorRow.LastModified.String,
		}
	}

	if len(pages) == 0 {
		return nil, nil
	}

	linkPairs, err := store.queries.ListInternalLinkPairsForCrawl(ctx, baselineCrawlID)
	if err != nil {
		return nil, fmt.Errorf("list internal link pairs for crawl: %w", err)
	}

	// Target URLs repeat heavily across pages (every nav and footer link appears
	// on every page), so intern them: one string per distinct target instead of
	// one per edge. On a 900-page site that is ~56k edges collapsing onto a few
	// hundred distinct strings.
	internedTargets := make(map[string]string)
	internalTargetsBySource := make(map[string][]string, len(pages))
	for _, linkPair := range linkPairs {
		interned, seen := internedTargets[linkPair.TargetUrl]
		if !seen {
			interned = linkPair.TargetUrl
			internedTargets[linkPair.TargetUrl] = interned
		}
		internalTargetsBySource[linkPair.SourceUrl] = append(internalTargetsBySource[linkPair.SourceUrl], interned)
	}

	return &Baseline{
		CrawlID:                 baselineCrawlID,
		pages:                   pages,
		internalTargetsBySource: internalTargetsBySource,
	}, nil
}

// internalTargets returns the baseline's internal link targets for one job URL.
// It is an in-memory lookup: no DB round trip happens on the runner's result
// loop, which would stall job dispatch for every page worker.
func (baseline *Baseline) internalTargets(jobURL string) []string {
	page, ok := baseline.lookup(jobURL)
	if !ok {
		return nil
	}

	return baseline.internalTargetsBySource[page.storedURL]
}

// PersistReusedResult copies one unchanged page's facts and outbound links from
// the baseline crawl into this crawl.
//
// The copy runs entirely in Postgres, so the page's stored response_time_ms and
// size_bytes carry over from the last real fetch. That is deliberate: a 304 is
// bodyless and therefore fast, and recording its timing would silently inflate
// every PageSpeed score on re-crawls. Only depth (this crawl's link position)
// and a freshly-issued ETag override the baseline values.
func (store *Store) PersistReusedResult(ctx context.Context, crawlID pgtype.UUID, baseline *Baseline, result CrawlResult) error {
	page, ok := baseline.lookup(result.Job.URL)
	if !ok {
		return fmt.Errorf("no baseline page for reused url %q", result.Job.URL)
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin reused crawl result transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txQueries := store.queries.WithTx(tx)
	copiedPageRows, err := txQueries.CopyCrawlPageFromBaseline(ctx, sqlc.CopyCrawlPageFromBaselineParams{
		CrawlID:         crawlID,
		Depth:           nullableInt4(result.Job.Depth),
		FreshEtag:       nullableText(result.Fetch.ETag),
		BaselineCrawlID: baseline.CrawlID,
		Url:             page.storedURL,
	})
	if err != nil {
		return fmt.Errorf("copy crawl page from baseline: %w", err)
	}
	// Zero rows has two causes: the page is already present in this crawl (a
	// benign replay, absorbed by ON CONFLICT), or the baseline row was removed
	// mid-crawl and the page is now silently missing from this crawl. Report the
	// fact without asserting which, since only the second is a problem and
	// distinguishing them would cost another query on the hot path.
	if copiedPageRows == 0 {
		log.Printf("reused page copy affected no rows (already present, or baseline row removed): url=%q baseline_crawl_id=%s", page.storedURL, baseline.CrawlID.String())
	}

	if _, err := txQueries.CopyCrawlLinksFromBaseline(ctx, sqlc.CopyCrawlLinksFromBaselineParams{
		CrawlID:         crawlID,
		BaselineCrawlID: baseline.CrawlID,
		SourceUrl:       page.storedURL,
	}); err != nil {
		return fmt.Errorf("copy crawl links from baseline: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reused crawl result transaction: %w", err)
	}

	return nil
}
