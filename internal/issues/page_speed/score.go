package pagespeed

import "github.com/ps-wizard/revserp/internal/issues/shared"

// Score builds the PageSpeed pillar score breakdown from persisted issue signals.
func Score(totalScoredPages int, crawlIssueSignals []shared.CrawlIssueSignal, psiInput *shared.GooglePSIScoreInput) shared.PillarScoreBreakdown {
	return shared.BuildPillarBreakdownWithOptions(PillarID, shared.PillarScoringConfig{Label: PillarLabel, Weight: PillarWeight, BucketWeights: BucketWeights, IssuePenaltyByType: IssuePenaltyByType}, shared.DefaultScoringMathConfig(), totalScoredPages, crawlIssueSignals, shared.IssueCoverage, psiInput)
}
