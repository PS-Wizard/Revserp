package issues

import (
	"math"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const scoringVersion = "v8-configurable"

// CalculateScores builds the persisted crawl scores from issue-derived pillar scoring.
func CalculateScores(crawlPageSignals []shared.CrawlPageSignal, crawlIssueSignals []shared.CrawlIssueSignal) shared.CrawlScores {
	return BuildScoreBreakdown("", crawlPageSignals, crawlIssueSignals).CrawlScores()
}

// BuildScoreBreakdown builds one persisted crawl score snapshot from current issue signals.
func BuildScoreBreakdown(crawlID string, crawlPageSignals []shared.CrawlPageSignal, crawlIssueSignals []shared.CrawlIssueSignal) shared.ScoreBreakdownSnapshot {
	return BuildScoreBreakdownWithConfig(crawlID, crawlPageSignals, crawlIssueSignals, DefaultScoringConfig())
}

// BuildScoreBreakdownWithConfig builds one score snapshot from an editable scoring config.
func BuildScoreBreakdownWithConfig(crawlID string, crawlPageSignals []shared.CrawlPageSignal, crawlIssueSignals []shared.CrawlIssueSignal, scoringConfig shared.ScoringConfig) shared.ScoreBreakdownSnapshot {
	totalScoredPages := shared.CountScoreablePages(crawlPageSignals)
	pillars := []shared.PillarScoreBreakdown{
		buildConfiguredPillarBreakdown(aeo.PillarID, scoringConfig, totalScoredPages, crawlIssueSignals),
		buildConfiguredPillarBreakdown(seo.PillarID, scoringConfig, totalScoredPages, crawlIssueSignals),
		buildConfiguredPillarBreakdown(pagespeed.PillarID, scoringConfig, totalScoredPages, crawlIssueSignals),
	}
	if totalScoredPages == 0 {
		for pillarIndex := range pillars {
			pillars[pillarIndex].Score = 0
			pillars[pillarIndex].WeightedContribution = 0
			pillars[pillarIndex].TotalPenalty = 0
			for bucketIndex := range pillars[pillarIndex].Buckets {
				pillars[pillarIndex].Buckets[bucketIndex].Score = 0
				pillars[pillarIndex].Buckets[bucketIndex].WeightedContribution = 0
				pillars[pillarIndex].Buckets[bucketIndex].TotalPenalty = 0
			}
		}
		return shared.ScoreBreakdownSnapshot{
			CrawlID:          crawlID,
			ScoringVersion:   scoringVersion,
			CoverageScale:    scoringConfig.CoverageScale,
			TotalScoredPages: 0,
			OverallScore:     0,
			Pillars:          pillars,
		}
	}

	snapshot := shared.ScoreBreakdownSnapshot{
		CrawlID:          crawlID,
		ScoringVersion:   scoringVersion,
		CoverageScale:    scoringConfig.CoverageScale,
		TotalScoredPages: int32(totalScoredPages),
		Pillars:          pillars,
	}
	crawlScores := snapshot.CrawlScores()
	snapshot.OverallScore = calculateOverallScore(crawlScores.SEOScore, crawlScores.AEOScore, crawlScores.PageSpeedScore, scoringConfig)
	return snapshot
}

func buildConfiguredPillarBreakdown(pillarID string, scoringConfig shared.ScoringConfig, totalScoredPages int, crawlIssueSignals []shared.CrawlIssueSignal) shared.PillarScoreBreakdown {
	pillarConfig := scoringConfig.Pillars[pillarID]
	issueCoverage := func(affectedPages int, totalScoredPages int) float64 {
		coverage := shared.IssueCoverageWithConfig(affectedPages, totalScoredPages, scoringConfig)
		if coverage > 0 && pillarConfig.MinimumIssueCoverage > 0 {
			return math.Max(coverage, pillarConfig.MinimumIssueCoverage)
		}
		return coverage
	}
	return shared.BuildPillarBreakdownWithOptions(pillarID, pillarConfig, scoringConfig, totalScoredPages, crawlIssueSignals, issueCoverage)
}

func calculateOverallScore(seoScore int32, aeoScore int32, pageSpeedScore int32, scoringConfig shared.ScoringConfig) int32 {
	minimumOverallScore := scoringConfig.MinimumOverallScore
	if minimumOverallScore <= 0 {
		minimumOverallScore = shared.MinimumOverallScore
	}
	overallScore := scoringConfig.OverallWeights[seo.PillarID]*float64(seoScore) + scoringConfig.OverallWeights[aeo.PillarID]*float64(aeoScore) + scoringConfig.OverallWeights[pagespeed.PillarID]*float64(pageSpeedScore)
	return shared.ClampScore(overallScore, minimumOverallScore)
}
