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

func TestLinkResolutionFailureAction(t *testing.T) {
	for _, test := range []struct {
		name    string
		halted  error
		context error
		want    error
	}{
		{"recoverable SQL error", nil, nil, nil},
		{"analysis expired", nil, context.DeadlineExceeded, errAnalysisDeadline},
		{"user cancelled", errCrawlStopped, context.Canceled, errCrawlStopped},
		{"cancel and deadline", errCrawlStopped, context.DeadlineExceeded, errCrawlStopped},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := resolveFailureAction(test.halted, test.context)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
