package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

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
	derivedIssueCount, _, err := store.DeriveIssuesWithPages(ctx, crawlID)
	return derivedIssueCount, err
}

// DeriveIssuesWithPages behaves like DeriveIssues but also returns the crawl pages it loaded,
// so a caller can pass them into ScoreCrawlWithPages instead of reloading the same heavy rows.
func (store *Store) DeriveIssuesWithPages(ctx context.Context, crawlID pgtype.UUID) (int, []sqlc.ListCrawlPagesForCrawlRow, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, nil, fmt.Errorf("begin derive issues transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txQueries := store.queries.WithTx(tx)
	loadStartedAt := time.Now()
	crawlPages, pageFacts, linkFacts, err := loadFactsWithPages(ctx, txQueries, crawlID)
	if err != nil {
		return 0, nil, err
	}
	seo.EnrichPageFactsWithContentFingerprints(pageFacts)
	if err := persistPageContentFingerprints(ctx, txQueries, pageFacts); err != nil {
		return 0, nil, err
	}
	loadElapsed := time.Since(loadStartedAt)

	computeStartedAt := time.Now()
	derivedIssues := DeriveIssues(pageFacts, linkFacts)
	computeElapsed := time.Since(computeStartedAt)
	log.Printf("derive timing: crawl_id=%s pages=%d load+fingerprint=%s compute=%s issues=%d", crawlID.String(), len(pageFacts), loadElapsed.Round(time.Millisecond), computeElapsed.Round(time.Millisecond), len(derivedIssues))
	if err := txQueries.DeleteCrawlIssuesForCrawl(ctx, crawlID); err != nil {
		return 0, nil, fmt.Errorf("delete crawl issues: %w", err)
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
		return 0, nil, fmt.Errorf("bulk create crawl issues: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("commit derive issues transaction: %w", err)
	}
	return len(derivedIssues), crawlPages, nil
}

// ScoreCrawl reloads one crawl's persisted signals, calculates scores, and stores them on the crawl row.
func (store *Store) ScoreCrawl(ctx context.Context, crawlID pgtype.UUID, psiInput *shared.GooglePSIScoreInput) (shared.CrawlScores, error) {
	crawlPages, err := store.queries.ListCrawlPagesForCrawl(ctx, crawlID)
	if err != nil {
		return shared.CrawlScores{}, fmt.Errorf("list crawl pages: %w", err)
	}
	return store.ScoreCrawlWithPages(ctx, crawlID, crawlPages, psiInput)
}

// ScoreCrawlWithPages behaves like ScoreCrawl but accepts already-loaded crawl pages (e.g. from
// DeriveIssuesWithPages) instead of reloading the same heavy visible_text/og_tags/json_ld rows.
func (store *Store) ScoreCrawlWithPages(ctx context.Context, crawlID pgtype.UUID, crawlPages []sqlc.ListCrawlPagesForCrawlRow, psiInput *shared.GooglePSIScoreInput) (shared.CrawlScores, error) {
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

	scoringConfig, err := LoadActiveScoringConfig(ctx, store.queries)
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
