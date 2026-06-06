package aeo

import (
	"math"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const minimumAEOIssueCoverage = 0.75

// Score builds the AEO pillar score breakdown from persisted issue signals.
func Score(totalScoredPages int, crawlIssueSignals []shared.CrawlIssueSignal) shared.PillarScoreBreakdown {
	return shared.BuildPillarBreakdownWithIssueCoverage(PillarID, PillarLabel, PillarWeight, BucketWeights, IssuePenaltyByType, totalScoredPages, crawlIssueSignals, aeoIssueCoverage)
}

func aeoIssueCoverage(affectedPages int, totalScoredPages int) float64 {
	baseCoverage := shared.IssueCoverage(affectedPages, totalScoredPages)
	if baseCoverage == 0 {
		return 0
	}
	return math.Max(baseCoverage, minimumAEOIssueCoverage)
}
