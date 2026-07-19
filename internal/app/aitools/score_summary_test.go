package aitools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

type fakeScoreSummaryReader struct {
	gotCrawlArg     sqlc.GetCrawlByIDForUserParams
	gotBreakdownArg sqlc.GetCrawlScoreBreakdownByCrawlForUserParams
	crawlRow        sqlc.GetCrawlByIDForUserRow
	breakdownRow    sqlc.CrawlScoreBreakdown
}

func (f *fakeScoreSummaryReader) GetCrawlByIDForUser(_ context.Context, arg sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error) {
	f.gotCrawlArg = arg
	return f.crawlRow, nil
}

func (f *fakeScoreSummaryReader) GetCrawlScoreBreakdownByCrawlForUser(_ context.Context, arg sqlc.GetCrawlScoreBreakdownByCrawlForUserParams) (sqlc.CrawlScoreBreakdown, error) {
	f.gotBreakdownArg = arg
	return f.breakdownRow, nil
}

func TestExecGetScoreSummary_UsesScopeIDsAndSummarizesBuckets(t *testing.T) {
	crawlID := testUUID(3)
	userID := testUUID(4)

	snapshot := issueshared.ScoreBreakdownSnapshot{
		Pillars: []issueshared.PillarScoreBreakdown{
			{
				ID: "seo", Label: "SEO", Score: 72,
				Buckets: []issueshared.BucketScoreBreakdown{
					{ID: "serp_metadata", Label: "SERP Metadata", Score: 60, AffectedURLCount: 5},
				},
			},
		},
	}
	breakdownJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeScoreSummaryReader{
		crawlRow:     sqlc.GetCrawlByIDForUserRow{OverallScore: pgtype.Int4{Int32: 80, Valid: true}},
		breakdownRow: sqlc.CrawlScoreBreakdown{BreakdownJson: breakdownJSON},
	}

	result, err := execGetScoreSummary(context.Background(), crawlID, userID, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotCrawlArg.ID != crawlID || fake.gotCrawlArg.UserID != userID {
		t.Fatalf("expected crawl lookup to use scope IDs, got %+v", fake.gotCrawlArg)
	}
	if fake.gotBreakdownArg.CrawlID != crawlID || fake.gotBreakdownArg.UserID != userID {
		t.Fatalf("expected breakdown lookup to use scope IDs, got %+v", fake.gotBreakdownArg)
	}

	var output scoreSummaryOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if output.OverallScore != 80 {
		t.Errorf("expected overall score 80, got %d", output.OverallScore)
	}
	if len(output.Pillars) != 1 || len(output.Pillars[0].Buckets) != 1 {
		t.Fatalf("expected one pillar with one bucket, got %+v", output.Pillars)
	}
	if output.Pillars[0].Buckets[0].AffectedURLCount != 5 {
		t.Errorf("expected bucket affected url count 5, got %+v", output.Pillars[0].Buckets[0])
	}
}
