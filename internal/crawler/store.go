package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

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

// PersistResult stores one processed crawl result and its discovered links.
func (store *Store) PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error {
	if result.ProcessErr != nil {
		return result.ProcessErr
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin crawl result transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	txQueries := store.queries.WithTx(tx)
	if _, err := txQueries.CreateCrawlPage(ctx, buildCreateCrawlPageParams(crawlID, rootURL, result)); err != nil {
		return fmt.Errorf("create crawl page: %w", err)
	}

	for _, parsedLink := range dedupeParsedLinks(result.ParsedPage) {
		if _, err := txQueries.CreateCrawlLink(ctx, buildCreateCrawlLinkParams(crawlID, result, parsedLink)); err != nil {
			return fmt.Errorf("create crawl link: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit crawl result transaction: %w", err)
	}

	return nil
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
		CrawlID:   crawlID,
		Url:       result.Fetch.FinalURL,
		StatusCode: nullableInt4(result.Fetch.StatusCode),
		ContentType: nullableText(result.Fetch.ContentType),
		SizeBytes:   nullableInt4(result.Fetch.ResponseSize),
		IsInternal:  nullableBool(isResultInternal(rootURL, result)),
		Depth:       nullableInt4(result.Job.Depth),
		Title:       nullableText(extractPageTitle(parsedPage)),
		MetaDescription: nullableText(extractMetaDescription(parsedPage)),
		H1:              nullableText(extractPageH1(parsedPage)),
		H1Count:         nullableInt4(extractPageH1Count(parsedPage)),
		H2Count:         nullableInt4(len(h2Headings)),
		H3Count:         nullableInt4(len(h3Headings)),
		// TODO: Replace this heading-only fallback with real visible-body text extraction.
		WordCount: nullableInt4(countWords(extractPageVisibleText(parsedPage))),
		Author:    pgtype.Text{},
		CanonicalUrl: nullableText(extractCanonicalURL(parsedPage)),
		Lang:         nullableText(extractPageLang(parsedPage)),
		// TODO: Extract viewport from the parsed document.
		Viewport: pgtype.Text{},
		Robots:   nullableText(extractPageRobots(parsedPage)),
		// TODO: Extract real image counts from the parsed document.
		ImageCount:              pgtype.Int4{},
		ImagesWithoutAltCount:   pgtype.Int4{},
		ImagesWithoutDimensions: pgtype.Int4{},
		ExternalLinks:           nullableInt4(externalLinkCount),
		InternalLinks:           nullableInt4(internalLinkCount),
		ResponseTimeMs:          nullableInt4(int(result.Fetch.ResponseTime.Milliseconds())),
		// TODO: Set this from the renderer path once Obscura fallback is wired in.
		JavascriptRendered: nullableBool(false),
		H2Headings:         mustMarshalJSON(h2Headings),
		H3Headings:         mustMarshalJSON(h3Headings),
		// TODO: Extract a real heading outline from the parsed document.
		HeadingOutline: mustMarshalJSON(nil),
		// TODO: Extract Open Graph tags from the parsed document.
		OgTags: mustMarshalJSON(nil),
		// TODO: Extract JSON-LD blocks from the parsed document.
		JsonLd: mustMarshalJSON(nil),
	}
}

// buildCreateCrawlLinkParams maps one parsed link into crawl_links insert params.
func buildCreateCrawlLinkParams(crawlID pgtype.UUID, result CrawlResult, parsedLink ParsedLink) sqlc.CreateCrawlLinkParams {
	return sqlc.CreateCrawlLinkParams{
		CrawlID:      crawlID,
		SourceUrl:    result.Fetch.FinalURL,
		TargetUrl:    parsedLink.TargetURL,
		AnchorText:   nullableText(parsedLink.AnchorText),
		IsInternal:   nullableBool(parsedLink.IsInternal),
		TargetStatus: pgtype.Int4{},
		Nofollow:     nullableBool(parsedLink.NoFollow),
	}
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

// extractPageVisibleText returns basic visible text derived from parsed headings.
func extractPageVisibleText(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	textParts := make([]string, 0, 1+len(parsedPage.H2Headings)+len(parsedPage.H3Headings))
	if parsedPage.H1 != "" {
		textParts = append(textParts, parsedPage.H1)
	}
	textParts = append(textParts, parsedPage.H2Headings...)
	textParts = append(textParts, parsedPage.H3Headings...)

	return strings.Join(textParts, " ")
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
