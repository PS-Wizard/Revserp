package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode"

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

// PersistResult stores one processed crawl result and its discovered links.
func (store *Store) PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin crawl result transaction: %w", err)
	}
	defer tx.Rollback(ctx)

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
		Author:                  pgtype.Text{},
		CanonicalUrl:            nullableText(extractCanonicalURL(parsedPage)),
		Lang:                    nullableText(extractPageLang(parsedPage)),
		Viewport:                nullableText(extractPageViewport(parsedPage)),
		Robots:                  nullableText(extractPageRobots(parsedPage)),
		ImageCount:              nullableInt4(extractPageImageCount(parsedPage)),
		ImagesWithoutAltCount:   nullableInt4(extractPageImagesWithoutAltCount(parsedPage)),
		ImagesWithoutDimensions: nullableInt4(extractPageImagesWithoutDimensions(parsedPage)),
		ExternalLinks:           nullableInt4(externalLinkCount),
		InternalLinks:           nullableInt4(internalLinkCount),
		ResponseTimeMs:          nullableInt4(int(result.Fetch.ResponseTime.Milliseconds())),
		// TODO: Set this from the renderer path once Obscura fallback is wired in.
		JavascriptRendered: nullableBool(false),
		H2Headings:         mustMarshalJSON(h2Headings),
		H3Headings:         mustMarshalJSON(h3Headings),
		// TODO: Extract a real heading outline from the parsed document.
		HeadingOutline: mustMarshalJSON(nil),
		OgTags:         mustMarshalJSON(extractPageOGTags(parsedPage)),
		JsonLd:         mustMarshalJSON(extractPageJSONLDBlocks(parsedPage)),
	}
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

// countParsedLinks returns internal and external link counts for one parsed page.
func countParsedLinks(parsedPage *ParsedPage) (int, int) {
	if parsedPage == nil {
		return 0, 0
	}

	internalLinkCount := 0
	externalLinkCount := 0
	for _, parsedLink := range parsedPage.Links {
		if parsedLink.IsInternal {
			internalLinkCount++
			continue
		}

		externalLinkCount++
	}

	return internalLinkCount, externalLinkCount
}

// extractPageTitle returns the parsed page title when available.
func extractPageTitle(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Title
}

// extractMetaDescription returns the parsed meta description when available.
func extractMetaDescription(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.MetaDescription
}

// extractCanonicalURL returns the parsed canonical URL when available.
func extractCanonicalURL(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.CanonicalURL
}

// extractPageLang returns the parsed language when available.
func extractPageLang(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Lang
}

// extractPageViewport returns the parsed viewport value when available.
func extractPageViewport(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Viewport
}

// extractPageRobots returns the parsed robots value when available.
func extractPageRobots(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Robots
}

// extractPageH1 returns the parsed first h1 when available.
func extractPageH1(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.H1
}

// extractPageH1Count returns the parsed h1 count when available.
func extractPageH1Count(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.H1Count
}

// extractPageOGTags returns parsed Open Graph tags when available.
func extractPageOGTags(parsedPage *ParsedPage) map[string]string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.OGTags
}

// extractPageJSONLDBlocks returns parsed JSON-LD blocks when available.
func extractPageJSONLDBlocks(parsedPage *ParsedPage) []string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.JSONLDBlocks
}

// extractPageImageCount returns the parsed image count when available.
func extractPageImageCount(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.ImageCount
}

// extractPageImagesWithoutAltCount returns the parsed missing-alt image count when available.
func extractPageImagesWithoutAltCount(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.ImagesWithoutAltCount
}

// extractPageImagesWithoutDimensions returns the parsed missing-dimensions image count when available.
func extractPageImagesWithoutDimensions(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.ImagesWithoutDimensions
}

// extractH2Headings returns parsed h2 headings when available.
func extractH2Headings(parsedPage *ParsedPage) []string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.H2Headings
}

// extractH3Headings returns parsed h3 headings when available.
func extractH3Headings(parsedPage *ParsedPage) []string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.H3Headings
}

// extractPageVisibleText returns parsed visible body text when available.
func extractPageVisibleText(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.VisibleText
}

// isResultInternal reports whether a fetched result URL belongs to the crawl root host.
func isResultInternal(rootURL string, result CrawlResult) bool {
	normalizedRootURL, rootErr := NormalizeURL(rootURL, nil)
	if rootErr != nil {
		return false
	}

	normalizedResultURL, resultErr := NormalizeURL(result.Fetch.FinalURL, nil)
	if resultErr != nil {
		return false
	}

	return IsInternalURL(normalizedRootURL, normalizedResultURL)
}

// countWords returns a simple whitespace-based word count.
func countWords(value string) int {
	wordCount := 0
	insideWord := false

	for _, character := range value {
		if unicode.IsSpace(character) {
			insideWord = false
			continue
		}

		if insideWord {
			continue
		}

		insideWord = true
		wordCount++
	}

	return wordCount
}

// nullableFetchStatusCode builds a valid pgtype.Int4 for an HTTP status code when available.
func nullableFetchStatusCode(value int) pgtype.Int4 {
	if value <= 0 {
		return pgtype.Int4{}
	}

	return nullableInt4(value)
}

// nullableFetchResponseSize builds a valid pgtype.Int4 for a response size when available.
func nullableFetchResponseSize(value int) pgtype.Int4 {
	if value <= 0 {
		return pgtype.Int4{}
	}

	return nullableInt4(value)
}

// nullableText builds a valid pgtype.Text from a non-empty string.
func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{String: value, Valid: true}
}

// nullableInt4 builds a valid pgtype.Int4 from an int value.
func nullableInt4(value int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(value), Valid: true}
}

// nullableBool builds a valid pgtype.Bool from a bool value.
func nullableBool(value bool) pgtype.Bool {
	return pgtype.Bool{Bool: value, Valid: true}
}

// mustMarshalJSON encodes a value into JSON bytes for jsonb columns.
func mustMarshalJSON(value any) []byte {
	encodedValue, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal crawler json value: %v", err))
	}

	return encodedValue
}
