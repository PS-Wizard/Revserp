package aitools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

// scoreSummaryReader is the narrow DB port get_score_summary depends on.
type scoreSummaryReader interface {
	GetCrawlByIDForUser(ctx context.Context, arg sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error)
	GetCrawlScoreBreakdownByCrawlForUser(ctx context.Context, arg sqlc.GetCrawlScoreBreakdownByCrawlForUserParams) (sqlc.CrawlScoreBreakdown, error)
}

func scoreSummaryTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "get_score_summary",
			Description: "Get the overall score and per-pillar (SEO, AEO, PageSpeed) scores for the current crawl, plus a summary of each pillar's top-level buckets. Does not include the full issue tree. Takes no arguments.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		Execute: func(ctx context.Context, _ json.RawMessage, s Scope) (Result, error) {
			return execGetScoreSummary(ctx, s.CrawlID, s.UserID, s.Queries)
		},
	}
}

type bucketSummary struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	Score            int32  `json:"score"`
	AffectedURLCount int32  `json:"affected_url_count"`
}

type pillarSummary struct {
	ID      string          `json:"id"`
	Label   string          `json:"label"`
	Score   int32           `json:"score"`
	Buckets []bucketSummary `json:"buckets"`
}

type scoreSummaryOutput struct {
	OverallScore int32           `json:"overall_score"`
	Pillars      []pillarSummary `json:"pillars"`
}

func execGetScoreSummary(ctx context.Context, crawlID pgtype.UUID, userID pgtype.UUID, reader scoreSummaryReader) (Result, error) {
	crawl, err := reader.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jsonResult(scoreSummaryOutput{}, "crawl not found")
		}
		return Result{}, err
	}

	output := scoreSummaryOutput{OverallScore: crawl.OverallScore.Int32}

	breakdownRow, err := reader.GetCrawlScoreBreakdownByCrawlForUser(ctx, sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Scores exist without a breakdown snapshot (should not normally
			// happen, but degrade gracefully rather than erroring).
			return jsonResult(output, "score summary (no bucket breakdown available)")
		}
		return Result{}, err
	}

	var snapshot issueshared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(breakdownRow.BreakdownJson, &snapshot); err != nil {
		return Result{}, err
	}

	for _, pillar := range snapshot.Pillars {
		pillarOut := pillarSummary{ID: pillar.ID, Label: pillar.Label, Score: pillar.Score}
		for _, bucket := range pillar.Buckets {
			pillarOut.Buckets = append(pillarOut.Buckets, bucketSummary{
				ID:               bucket.ID,
				Label:            bucket.Label,
				Score:            bucket.Score,
				AffectedURLCount: bucket.AffectedURLCount,
			})
		}
		output.Pillars = append(output.Pillars, pillarOut)
	}

	return jsonResult(output, "score summary loaded")
}
