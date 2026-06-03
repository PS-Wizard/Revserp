package analyzer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Store derives and persists crawl issues from persisted crawl pages.
type Store struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

// NewStore builds an analyzer store from a Postgres pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		pool:    pool,
		queries: sqlc.New(pool),
	}
}

// DeriveIssues reloads one crawl's pages, derives issues, and replaces persisted issue rows.
func (store *Store) DeriveIssues(ctx context.Context, crawlID pgtype.UUID) (int, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin derive issues transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	txQueries := store.queries.WithTx(tx)
	crawlPages, err := txQueries.ListCrawlPagesForCrawl(ctx, crawlID)
	if err != nil {
		return 0, fmt.Errorf("list crawl pages: %w", err)
	}
	internalCrawlLinks, err := txQueries.ListInternalCrawlLinksForCrawl(ctx, crawlID)
	if err != nil {
		return 0, fmt.Errorf("list internal crawl links: %w", err)
	}

	pageFacts := make([]PageFact, 0, len(crawlPages))
	for _, crawlPage := range crawlPages {
		pageFacts = append(pageFacts, newPageFact(crawlPage))
	}

	linkFacts := make([]LinkFact, 0, len(internalCrawlLinks))
	for _, internalCrawlLink := range internalCrawlLinks {
		linkFacts = append(linkFacts, LinkFact{SourceURL: internalCrawlLink.SourceUrl, TargetURL: internalCrawlLink.TargetUrl})
	}

	derivedIssues := DeriveIssues(pageFacts, linkFacts)
	if err := txQueries.DeleteCrawlIssuesForCrawl(ctx, crawlID); err != nil {
		return 0, fmt.Errorf("delete crawl issues: %w", err)
	}

	for _, derivedIssue := range derivedIssues {
		if _, err := txQueries.CreateCrawlIssue(ctx, sqlc.CreateCrawlIssueParams{
			CrawlID:     crawlID,
			CrawlPageID: derivedIssue.CrawlPageID,
			Url:         derivedIssue.URL,
			Severity:    derivedIssue.Severity,
			Category:    derivedIssue.Category,
			Code:        derivedIssue.Code,
			Message:     derivedIssue.Message,
			Details:     derivedIssue.Details,
		}); err != nil {
			return 0, fmt.Errorf("create crawl issue %q for %q: %w", derivedIssue.Code, derivedIssue.URL, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit derive issues transaction: %w", err)
	}

	return len(derivedIssues), nil
}

// newPageFact maps one persisted crawl page row into issue-derivation input.
func newPageFact(crawlPage sqlc.ListCrawlPagesForCrawlRow) PageFact {
	return PageFact{
		ID:                      crawlPage.ID,
		URL:                     crawlPage.Url,
		Depth:                   int32Value(crawlPage.Depth),
		Title:                   textValue(crawlPage.Title),
		MetaDescription:         textValue(crawlPage.MetaDescription),
		H1:                      textValue(crawlPage.H1),
		H1Count:                 int32Value(crawlPage.H1Count),
		H2Count:                 int32Value(crawlPage.H2Count),
		WordCount:               int32Value(crawlPage.WordCount),
		CanonicalURL:            textValue(crawlPage.CanonicalUrl),
		Viewport:                textValue(crawlPage.Viewport),
		Lang:                    textValue(crawlPage.Lang),
		Robots:                  textValue(crawlPage.Robots),
		StatusCode:              int32Value(crawlPage.StatusCode),
		SizeBytes:               int32Value(crawlPage.SizeBytes),
		ImageCount:              int32Value(crawlPage.ImageCount),
		ImagesWithoutAltCount:   int32Value(crawlPage.ImagesWithoutAltCount),
		ImagesWithoutDimensions: int32Value(crawlPage.ImagesWithoutDimensions),
		ResponseTimeMs:          int32Value(crawlPage.ResponseTimeMs),
		OGTags:                  crawlPage.OgTags,
		JSONLD:                  crawlPage.JsonLd,
	}
}

// textValue unwraps pg text values used by issue derivation.
func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// int32Value unwraps pg int values used by issue derivation.
func int32Value(value pgtype.Int4) int32 {
	if !value.Valid {
		return 0
	}
	return value.Int32
}
