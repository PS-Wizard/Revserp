package scoring

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Store loads crawl scoring inputs, calculates top-level crawl scores, and persists them.
type Store struct {
	queries *sqlc.Queries
}

// NewStore builds a scoring store from a Postgres pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlc.New(pool)}
}

// ScoreCrawl reloads one crawl's persisted signals, calculates scores, and stores them on the crawl row.
func (store *Store) ScoreCrawl(ctx context.Context, crawlID pgtype.UUID) (CrawlScores, error) {
	crawlPages, err := store.queries.ListCrawlPagesForCrawl(ctx, crawlID)
	if err != nil {
		return CrawlScores{}, fmt.Errorf("list crawl pages: %w", err)
	}
	crawlIssues, err := store.queries.ListCrawlIssuesForCrawl(ctx, crawlID)
	if err != nil {
		return CrawlScores{}, fmt.Errorf("list crawl issues: %w", err)
	}

	crawlPageSignals := make([]CrawlPageSignal, 0, len(crawlPages))
	for _, crawlPage := range crawlPages {
		crawlPageSignals = append(crawlPageSignals, CrawlPageSignal{
			URL:            crawlPage.Url,
			StatusCode:     int32Value(crawlPage.StatusCode),
			ContentType:    textValue(crawlPage.ContentType),
			WordCount:      int32Value(crawlPage.WordCount),
			ResponseTimeMs: int32Value(crawlPage.ResponseTimeMs),
			SizeBytes:      int32Value(crawlPage.SizeBytes),
			OGTags:         crawlPage.OgTags,
			JSONLD:         crawlPage.JsonLd,
		})
	}

	crawlIssueSignals := make([]CrawlIssueSignal, 0, len(crawlIssues))
	for _, crawlIssue := range crawlIssues {
		crawlIssueSignals = append(crawlIssueSignals, CrawlIssueSignal{
			URL:       crawlIssue.Url,
			Severity:  crawlIssue.Severity,
			IssueType: crawlIssue.IssueType,
		})
	}

	crawlScores := CalculateScores(crawlPageSignals, crawlIssueSignals)
	if err := store.queries.UpdateCrawlScores(ctx, sqlc.UpdateCrawlScoresParams{
		ID:             crawlID,
		SeoScore:       pgtype.Int4{Int32: crawlScores.SEOScore, Valid: true},
		AeoScore:       pgtype.Int4{Int32: crawlScores.AEOScore, Valid: true},
		PagespeedScore: pgtype.Int4{Int32: crawlScores.PageSpeedScore, Valid: true},
		OverallScore:   pgtype.Int4{Int32: crawlScores.OverallScore, Valid: true},
	}); err != nil {
		return CrawlScores{}, fmt.Errorf("update crawl scores: %w", err)
	}

	return crawlScores, nil
}

// textValue unwraps pg text values used by crawl scoring.
func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// int32Value unwraps pg int values used by crawl scoring.
func int32Value(value pgtype.Int4) int32 {
	if !value.Valid {
		return 0
	}
	return value.Int32
}
