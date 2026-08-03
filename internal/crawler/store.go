package crawler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const insertCrawlLinkSQL = `
INSERT INTO crawl_links (
    crawl_id,
    source_url,
    target_url,
    anchor_text,
    is_internal,
    target_status,
    nofollow
) VALUES ($1, $2, $3, $4, $5, $6, $7)
`

// Store persists crawler results into crawl_pages and crawl_links.
type Store struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

// NewStore builds a crawler store from a Postgres pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		pool:    pool,
		queries: sqlc.New(pool),
	}
}

// MarkCrawlRunning marks a crawl as started.
func (store *Store) MarkCrawlRunning(ctx context.Context, crawlID pgtype.UUID) error {
	if err := store.queries.MarkCrawlRunning(ctx, crawlID); err != nil {
		return fmt.Errorf("mark crawl running: %w", err)
	}

	return nil
}

// MarkCrawlCompleted writes final crawl counters and completion status.
func (store *Store) MarkCrawlCompleted(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error {
	if err := store.queries.MarkCrawlCompleted(ctx, sqlc.MarkCrawlCompletedParams{
		ID:              crawlID,
		UrlsDiscovered:  int32(urlsDiscovered),
		UrlsCrawled:     int32(urlsCrawled),
		MaxDepthReached: int32(maxDepthReached),
	}); err != nil {
		return fmt.Errorf("mark crawl completed: %w", err)
	}

	return nil
}

// MarkCrawlFailed writes final crawl counters and failed status.
func (store *Store) MarkCrawlFailed(ctx context.Context, crawlID pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int) error {
	if err := store.queries.MarkCrawlFailed(ctx, sqlc.MarkCrawlFailedParams{
		ID:              crawlID,
		UrlsDiscovered:  int32(urlsDiscovered),
		UrlsCrawled:     int32(urlsCrawled),
		MaxDepthReached: int32(maxDepthReached),
	}); err != nil {
		return fmt.Errorf("mark crawl failed: %w", err)
	}

	return nil
}

// UpdateCrawlPhase writes the current sub-phase for a running crawl.
func (store *Store) UpdateCrawlPhase(ctx context.Context, crawlID pgtype.UUID, phase string) error {
	if err := store.queries.UpdateCrawlPhase(ctx, sqlc.UpdateCrawlPhaseParams{
		ID:    crawlID,
		Phase: pgtype.Text{String: phase, Valid: true},
	}); err != nil {
		return fmt.Errorf("update crawl phase: %w", err)
	}

	return nil
}

// UpdateCrawlProgress writes in-progress crawl counters and reports whether the
// crawl is still running. The UPDATE is gated on status = 'running', so zero
// rows affected means the crawl was cancelled (or otherwise ended) out-of-band.
func (store *Store) UpdateCrawlProgress(ctx context.Context, crawlID pgtype.UUID, urlsCrawled int, urlsDiscovered int) (bool, error) {
	rows, err := store.queries.UpdateCrawlProgress(ctx, sqlc.UpdateCrawlProgressParams{
		ID:             crawlID,
		UrlsCrawled:    int32(urlsCrawled),
		UrlsDiscovered: int32(urlsDiscovered),
	})
	if err != nil {
		return false, fmt.Errorf("update crawl progress: %w", err)
	}

	return rows > 0, nil
}

// PersistResult stores one processed crawl result and its discovered links.
func (store *Store) PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin crawl result transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txQueries := store.queries.WithTx(tx)
	if _, err := txQueries.CreateCrawlPage(ctx, buildCreateCrawlPageParams(crawlID, rootURL, result)); err != nil {
		if isUniqueViolation(err) {
			return nil
		}

		return fmt.Errorf("create crawl page: %w", err)
	}

	if result.ProcessErr == nil {
		if err := store.insertCrawlLinksBatch(ctx, tx, crawlID, result, dedupeParsedLinks(result.ParsedPage)); err != nil {
			return fmt.Errorf("create crawl links batch: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit crawl result transaction: %w", err)
	}

	return nil
}

// ResolveInternalLinkTargetStatuses fills target_status on this crawl's internal
// links from the pages the crawl actually fetched. It must run after the crawl
// loop finishes: a link row is normally written before its target has been
// fetched, so resolving inline would leave almost every value NULL. Until this
// runs, broken-internal-link derivation has nothing to work from.
func (store *Store) ResolveInternalLinkTargetStatuses(ctx context.Context, crawlID pgtype.UUID) (int64, error) {
	rows, err := store.queries.ResolveInternalLinkTargetStatuses(ctx, crawlID)
	if err != nil {
		return 0, fmt.Errorf("resolve internal link target statuses: %w", err)
	}
	return rows, nil
}

// isUniqueViolation reports whether an error is a Postgres unique constraint violation.
func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}

	return postgresError.Code == "23505"
}

// buildCreateCrawlPageParams maps one crawl result into crawl_pages insert params.
func buildCreateCrawlPageParams(crawlID pgtype.UUID, rootURL string, result CrawlResult) sqlc.CreateCrawlPageParams {
	var parsedPage *ParsedPage
	if result.ParsedPage != nil {
		parsedPage = result.ParsedPage
	}

	internalLinkCount, externalLinkCount := countParsedLinks(parsedPage)
	h2Headings := extractH2Headings(parsedPage)
	h3Headings := extractH3Headings(parsedPage)
	headingOutline := extractParsedHeadingOutline(parsedPage)

	return sqlc.CreateCrawlPageParams{
		CrawlID:                 crawlID,
		Url:                     crawlPageURL(result),
		StatusCode:              nullableFetchStatusCode(result.Fetch.StatusCode),
		ContentType:             nullableText(result.Fetch.ContentType),
		SizeBytes:               nullableFetchResponseSize(result.Fetch.ResponseSize),
		IsInternal:              nullableBool(isResultInternal(rootURL, result)),
		Depth:                   nullableInt4(result.Job.Depth),
		Title:                   nullableText(extractPageTitle(parsedPage)),
		MetaDescription:         nullableText(extractMetaDescription(parsedPage)),
		H1:                      nullableText(extractPageH1(parsedPage)),
		H1Count:                 nullableInt4(extractPageH1Count(parsedPage)),
		H2Count:                 nullableInt4(len(h2Headings)),
		H3Count:                 nullableInt4(len(h3Headings)),
		WordCount:               nullableInt4(countWords(extractPageVisibleText(parsedPage))),
		VisibleText:             nullableText(extractPageVisibleText(parsedPage)),
		ContentSha256:           pgtype.Text{},
		Author:                  nullableText(extractPageAuthor(parsedPage)),
		Lang:                    nullableText(extractPageLang(parsedPage)),
		Viewport:                nullableText(extractPageViewport(parsedPage)),
		Robots:                  nullableText(extractPageRobots(parsedPage)),
		ImageCount:              nullableInt4(extractPageImageCount(parsedPage)),
		ImagesWithoutAltCount:   nullableInt4(extractPageImagesWithoutAltCount(parsedPage)),
		ImagesWithoutDimensions: nullableInt4(extractPageImagesWithoutDimensions(parsedPage)),
		ExternalLinks:           nullableInt4(externalLinkCount),
		InternalLinks:           nullableInt4(internalLinkCount),
		ResponseTimeMs:          nullableInt4(int(result.Fetch.ResponseTime.Milliseconds())),
		JavascriptRendered:      nullableBool(result.JavascriptRendered),
		H2Headings:              mustMarshalJSON(h2Headings),
		H3Headings:              mustMarshalJSON(h3Headings),
		HeadingOutline:          mustMarshalJSON(headingOutline),
		OgTags:                  mustMarshalJSON(extractPageOGTags(parsedPage)),
		JsonLd:                  mustMarshalJSON(extractPageJSONLDBlocks(parsedPage)),
		// Stored verbatim so the next crawl can echo them back unchanged.
		Etag:         nullableText(result.Fetch.ETag),
		LastModified: nullableText(result.Fetch.LastModified),
		Soft404:      result.SoftNotFound,
		FetchError:   nullableText(fetchErrorMessage(result)),
	}
}

// fetchErrorMessage returns the reason a page could not be fetched, or an empty
// string when the fetch succeeded. A network failure persists as a page row with
// no status code, so without this marker it is indistinguishable from a page
// that genuinely has no title, meta description, or H1.
func fetchErrorMessage(result CrawlResult) string {
	if result.Fetch.FetchError != nil {
		return capErrorMessage(result.Fetch.FetchError.Error())
	}
	// A result with no status code and no parsed page never produced content,
	// even when no transport error surfaced.
	if result.Fetch.StatusCode == 0 && result.ParsedPage == nil && !result.NotModified {
		if result.ProcessErr != nil {
			return capErrorMessage(result.ProcessErr.Error())
		}
		return "page could not be fetched"
	}
	return ""
}

func capErrorMessage(message string) string {
	const maxFetchErrorLength = 500
	if len(message) > maxFetchErrorLength {
		return message[:maxFetchErrorLength]
	}
	return message
}

// crawlPageURL returns the best available URL to persist for one crawl result.
func crawlPageURL(result CrawlResult) string {
	if result.Fetch.FinalURL != "" {
		return result.Fetch.FinalURL
	}

	return result.Job.URL
}

// insertCrawlLinksBatch inserts one page's deduped links in a single pgx batch.
func (store *Store) insertCrawlLinksBatch(ctx context.Context, tx pgx.Tx, crawlID pgtype.UUID, result CrawlResult, parsedLinks []ParsedLink) error {
	if len(parsedLinks) == 0 {
		return nil
	}

	var batch pgx.Batch
	for _, parsedLink := range parsedLinks {
		batch.Queue(
			insertCrawlLinkSQL,
			crawlID,
			result.Fetch.FinalURL,
			parsedLink.TargetURL,
			nullableText(parsedLink.AnchorText),
			nullableBool(parsedLink.IsInternal),
			pgtype.Int4{},
			nullableBool(parsedLink.NoFollow),
		)
	}

	batchResults := tx.SendBatch(ctx, &batch)
	defer batchResults.Close()

	for range parsedLinks {
		if _, err := batchResults.Exec(); err != nil {
			return err
		}
	}

	return nil
}

// dedupeParsedLinks removes duplicate source-target-anchor combinations from one page result.
func dedupeParsedLinks(parsedPage *ParsedPage) []ParsedLink {
	if parsedPage == nil {
		return nil
	}

	seenLinkKeys := make(map[string]struct{})
	var dedupedLinks []ParsedLink

	for _, parsedLink := range parsedPage.Links {
		linkKey := parsedLink.TargetURL + "\n" + parsedLink.AnchorText
		if _, seen := seenLinkKeys[linkKey]; seen {
			continue
		}

		seenLinkKeys[linkKey] = struct{}{}
		dedupedLinks = append(dedupedLinks, parsedLink)
	}

	return dedupedLinks
}
