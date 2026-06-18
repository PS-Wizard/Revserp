package pagespeed

import (
	"testing"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func TestScore_OverridesPsiCwvBucketWithPSIScore(t *testing.T) {
	psiScore := 93
	psiInput := &shared.GooglePSIScoreInput{MobilePerformanceScore: &psiScore}

	breakdown := Score(1, nil, psiInput)

	psiBucket := findPsiCwvBucket(t, breakdown)
	if psiBucket.Score != 93 {
		t.Errorf("expected psi_cwv bucket score 93, got %d", psiBucket.Score)
	}
	if psiBucket.TotalPenalty != 7 {
		t.Errorf("expected psi_cwv bucket penalty 7, got %f", psiBucket.TotalPenalty)
	}
}

func TestScore_FallsBackToIssueScoringWithoutPSI(t *testing.T) {
	breakdown := Score(1, nil, nil)

	psiBucket := findPsiCwvBucket(t, breakdown)
	if psiBucket.Score != 100 {
		t.Errorf("expected psi_cwv bucket score 100 without PSI, got %d", psiBucket.Score)
	}
}

func findPsiCwvBucket(t *testing.T, breakdown shared.PillarScoreBreakdown) *shared.BucketScoreBreakdown {
	t.Helper()
	for i := range breakdown.Buckets {
		if breakdown.Buckets[i].ID == "psi_cwv" {
			return &breakdown.Buckets[i]
		}
	}
	t.Fatal("psi_cwv bucket not found")
	return nil
}
