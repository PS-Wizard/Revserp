package issues

import (
	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const scoringVersion = "v7"

// CalculateScores builds the persisted crawl scores from issue-derived pillar scoring.
func CalculateScores(crawlPageSignals []shared.CrawlPageSignal, crawlIssueSignals []shared.CrawlIssueSignal) shared.CrawlScores {
	return BuildScoreBreakdown("", crawlPageSignals, crawlIssueSignals).CrawlScores()
}

// BuildScoreBreakdown builds one persisted crawl score snapshot from current issue signals.
func BuildScoreBreakdown(crawlID string, crawlPageSignals []shared.CrawlPageSignal, crawlIssueSignals []shared.CrawlIssueSignal) shared.ScoreBreakdownSnapshot {
	totalScoredPages := shared.CountScoreablePages(crawlPageSignals)
	pillars := []shared.PillarScoreBreakdown{
		aeo.Score(totalScoredPages, crawlIssueSignals),
		seo.Score(totalScoredPages, crawlIssueSignals),
		pagespeed.Score(totalScoredPages, crawlIssueSignals),
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
			CoverageScale:    shared.CoverageScale,
			TotalScoredPages: 0,
			OverallScore:     0,
			Pillars:          pillars,
		}
	}

	snapshot := shared.ScoreBreakdownSnapshot{
		CrawlID:          crawlID,
		ScoringVersion:   scoringVersion,
		CoverageScale:    shared.CoverageScale,
		TotalScoredPages: int32(totalScoredPages),
		Pillars:          pillars,
	}
	crawlScores := snapshot.CrawlScores()
	snapshot.OverallScore = calculateOverallScore(crawlScores.SEOScore, crawlScores.AEOScore, crawlScores.PageSpeedScore)
	return snapshot
}

func calculateOverallScore(seoScore int32, aeoScore int32, pageSpeedScore int32) int32 {
	overallScore := 0.65*float64(seoScore) + 0.20*float64(aeoScore) + 0.15*float64(pageSpeedScore)
	return shared.ClampScore(overallScore, shared.MinimumOverallScore)
}
