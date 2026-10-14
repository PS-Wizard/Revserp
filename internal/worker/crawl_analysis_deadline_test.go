package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
)

func TestExpiredAnalysisDoesNotStartNextStage(t *testing.T) {
	for _, stage := range []string{
		crawler.AnalysisStageCheckingLinks,
		crawler.AnalysisStageDerivingIssues,
		crawler.AnalysisStageScoring,
	} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			reporter := &fakeProgressReporter{alive: true}
			err := reportAnalysisStage(ctx, reporter, pgtype.UUID{}, crawler.CrawlAnalysisProgress{Stage: stage}, func() error { return nil })
			if !errors.Is(err, errAnalysisDeadline) {
				t.Fatalf("error = %v, want analysis deadline", err)
			}
			if len(reporter.reports) != 0 {
				t.Fatalf("started %d reports after analysis expired", len(reporter.reports))
			}
		})
	}
}

func TestRootCancellationTakesPriorityOverAnalysisDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	reporter := &fakeProgressReporter{alive: true}
	err := reportAnalysisStage(ctx, reporter, pgtype.UUID{}, crawler.CrawlAnalysisProgress{Stage: crawler.AnalysisStageScoring}, func() error { return errCrawlStopped })
	if !errors.Is(err, errCrawlStopped) || errors.Is(err, errAnalysisDeadline) {
		t.Fatalf("error = %v, want stopped without deadline failure", err)
	}
	if len(reporter.reports) != 0 {
		t.Fatal("reported a new stage after root cancellation")
	}
}
