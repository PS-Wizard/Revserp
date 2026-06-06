package seo

import "github.com/ps-wizard/revserp/internal/issues/shared"

// Score builds the SEO pillar score breakdown from persisted issue signals.
func Score(totalScoredPages int, crawlIssueSignals []shared.CrawlIssueSignal) shared.PillarScoreBreakdown {
	return shared.BuildPillarBreakdown(PillarID, PillarLabel, PillarWeight, BucketWeights, IssuePenaltyByType, totalScoredPages, crawlIssueSignals)
}
