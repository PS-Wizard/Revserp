package aeo

import (
	"math"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// MinimumIssueCoverage is the minimum coverage floor applied to AEO issues to prevent tiny affected counts from being ignored.
// It is exported so that the config-aware scoring path can reference the same value via DefaultScoringConfig.
const MinimumIssueCoverage = 0.75

// Score builds the AEO pillar score breakdown from persisted issue signals.
func Score(totalScoredPages int, crawlIssueSignals []shared.CrawlIssueSignal) shared.PillarScoreBreakdown {
	return shared.BuildPillarBreakdownWithIssueCoverage(PillarID, PillarLabel, PillarWeight, BucketWeights, IssuePenaltyByType, totalScoredPages, crawlIssueSignals, aeoIssueCoverage)
}

func aeoIssueCoverage(affectedPages int, totalScoredPages int) float64 {
	baseCoverage := shared.IssueCoverage(affectedPages, totalScoredPages)
	if baseCoverage == 0 {
		return 0
	}
	return math.Max(baseCoverage, MinimumIssueCoverage)
}
