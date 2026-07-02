package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// Store derives issues and stores score outputs from persisted crawl facts.
type Store struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

// NewStore builds an issues store from a Postgres pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: sqlc.New(pool)}
}

// DeriveIssues reloads one crawl's pages, derives issues, and replaces persisted issue rows.
func (store *Store) DeriveIssues(ctx context.Context, crawlID pgtype.UUID) (int, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin derive issues transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txQueries := store.queries.WithTx(tx)
	pageFacts, linkFacts, err := loadFacts(ctx, txQueries, crawlID)
	if err != nil {
		return 0, err
	}
	seo.EnrichPageFactsWithContentFingerprints(pageFacts)
	if err := persistPageContentFingerprints(ctx, txQueries, pageFacts); err != nil {
		return 0, err
	}

	derivedIssues := DeriveIssues(pageFacts, linkFacts)
	if err := txQueries.DeleteCrawlIssuesForCrawl(ctx, crawlID); err != nil {
		return 0, fmt.Errorf("delete crawl issues: %w", err)
	}
	issueRows := make([]sqlc.CreateCrawlIssuesParams, 0, len(derivedIssues))
	for _, derivedIssue := range derivedIssues {
		issueRows = append(issueRows, sqlc.CreateCrawlIssuesParams{
			CrawlID:     crawlID,
			CrawlPageID: derivedIssue.CrawlPageID,
			Url:         derivedIssue.URL,
			Pillar:      derivedIssue.Pillar,
			Bucket:      derivedIssue.Bucket,
			IssueType:   derivedIssue.IssueType,
			Severity:    derivedIssue.Severity,
			Message:     derivedIssue.Message,
			Details:     derivedIssue.Details,
		})
	}
	if _, err := txQueries.CreateCrawlIssues(ctx, issueRows); err != nil {
		return 0, fmt.Errorf("bulk create crawl issues: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit derive issues transaction: %w", err)
	}
	return len(derivedIssues), nil
}

// ScoreCrawl reloads one crawl's persisted signals, calculates scores, and stores them on the crawl row.
func (store *Store) ScoreCrawl(ctx context.Context, crawlID pgtype.UUID, psiInput *shared.GooglePSIScoreInput) (shared.CrawlScores, error) {
	crawlPages, err := store.queries.ListCrawlPagesForCrawl(ctx, crawlID)
	if err != nil {
		return shared.CrawlScores{}, fmt.Errorf("list crawl pages: %w", err)
	}
	crawlIssues, err := store.queries.ListCrawlIssuesForCrawl(ctx, crawlID)
	if err != nil {
		return shared.CrawlScores{}, fmt.Errorf("list crawl issues: %w", err)
	}

	crawlPageSignals := make([]shared.CrawlPageSignal, 0, len(crawlPages))
	for _, crawlPage := range crawlPages {
		crawlPageSignals = append(crawlPageSignals, shared.CrawlPageSignal{
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

	crawlIssueSignals := make([]shared.CrawlIssueSignal, 0, len(crawlIssues))
	for _, crawlIssue := range crawlIssues {
		crawlIssueSignals = append(crawlIssueSignals, shared.CrawlIssueSignal{
			URL:       crawlIssue.Url,
			Pillar:    crawlIssue.Pillar,
			Bucket:    crawlIssue.Bucket,
			Severity:  crawlIssue.Severity,
			IssueType: crawlIssue.IssueType,
			Message:   crawlIssue.Message,
			Details:   crawlIssue.Details,
		})
	}

	orgID, err := store.queries.GetCrawlOrgID(ctx, crawlID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return shared.CrawlScores{}, fmt.Errorf("get crawl org id: %w", err)
	}

	scoringConfig, err := LoadEffectiveScoringConfig(ctx, store.queries, orgID)
	if err != nil {
		return shared.CrawlScores{}, err
	}

	crawlScoreBreakdown := BuildScoreBreakdownWithConfig(crawlID.String(), crawlPageSignals, crawlIssueSignals, scoringConfig, psiInput)
	crawlScores := crawlScoreBreakdown.CrawlScores()
	breakdownJSON, err := json.Marshal(crawlScoreBreakdown)
	if err != nil {
		return shared.CrawlScores{}, fmt.Errorf("marshal crawl score breakdown: %w", err)
	}

	if err := store.queries.UpdateCrawlScores(ctx, sqlc.UpdateCrawlScoresParams{
		ID:             crawlID,
		SeoScore:       pgtype.Int4{Int32: crawlScores.SEOScore, Valid: true},
		AeoScore:       pgtype.Int4{Int32: crawlScores.AEOScore, Valid: true},
		PagespeedScore: pgtype.Int4{Int32: crawlScores.PageSpeedScore, Valid: true},
		OverallScore:   pgtype.Int4{Int32: crawlScores.OverallScore, Valid: true},
	}); err != nil {
		return shared.CrawlScores{}, fmt.Errorf("update crawl scores: %w", err)
	}
	if err := store.queries.UpsertCrawlScoreBreakdown(ctx, sqlc.UpsertCrawlScoreBreakdownParams{
		CrawlID:        crawlID,
		ScoringVersion: scoringVersion,
		BreakdownJson:  breakdownJSON,
	}); err != nil {
		return shared.CrawlScores{}, fmt.Errorf("upsert crawl score breakdown: %w", err)
	}

	return crawlScores, nil
}
