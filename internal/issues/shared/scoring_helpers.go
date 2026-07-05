package shared

import (
	"math"
	"sort"
)

const (
	MinimumOverallScore = 1
	CoverageScale       = 8.0
	// SoftSumDecay is the geometric decay applied to each successive (lower) issue penalty
	// when combining issue penalties within a bucket. The worst issue counts in full, the
	// next at SoftSumDecay, the next at SoftSumDecay^2, and so on. This keeps a bucket from
	// free-falling just because several issue types coexist while still rewarding fixes to
	// the worst issue, and converges to roughly 1/(1-SoftSumDecay) times the worst penalty.
	SoftSumDecay = 0.5
)

// BuildPillarBreakdownWithOptions builds one pillar breakdown from editable scoring config.
func BuildPillarBreakdownWithOptions(pillarID string, pillarConfig PillarScoringConfig, scoringConfig ScoringConfig, totalScoredPages int, crawlIssueSignals []CrawlIssueSignal, issueCoverage func(affectedPages int, totalScoredPages int) float64, psiInput *GooglePSIScoreInput) PillarScoreBreakdown {
	issueGroupsByBucket := BuildIssueGroupsByBucket(pillarID, crawlIssueSignals)
	buckets := make([]BucketScoreBreakdown, 0, len(pillarConfig.BucketWeights))
	pillarAffectedURLs := make(map[string]struct{})
	issueTypeCount := int32(0)
	issueRowCount := int32(0)

	for _, bucketID := range SortedBucketIDs(pillarConfig.BucketWeights) {
		bucketBreakdown := BuildBucketBreakdownWithOptions(bucketID, pillarConfig.BucketWeights[bucketID], pillarConfig.IssuePenaltyByType, scoringConfig, totalScoredPages, issueGroupsByBucket[bucketID], issueCoverage)
		buckets = append(buckets, bucketBreakdown)
		issueTypeCount += bucketBreakdown.IssueTypeCount
		issueRowCount += bucketBreakdown.IssueRowCount
		for _, issueGroup := range issueGroupsByBucket[bucketID] {
			for affectedURL := range issueGroup.AffectedURLs {
				pillarAffectedURLs[affectedURL] = struct{}{}
			}
		}
	}

	weightedBucketScoreSum := 0.0
	for bucketIndex := range buckets {
		psiScore := resolvePSIBucketScore(buckets[bucketIndex].ID, psiInput)
		bucketScore := buckets[bucketIndex].Score
		if psiScore != nil {
			bucketScore = ClampScore(*psiScore, 0)
			buckets[bucketIndex].Score = bucketScore
			buckets[bucketIndex].TotalPenalty = RoundFloat64(float64(100-bucketScore), 2)
		}
		buckets[bucketIndex].WeightedContribution = RoundFloat64(float64(bucketScore)*buckets[bucketIndex].Weight, 2)
		weightedBucketScoreSum += float64(bucketScore) * buckets[bucketIndex].Weight
	}

	pillarScore := ClampScore(weightedBucketScoreSum, 0)
	return PillarScoreBreakdown{
		ID:                   pillarID,
		Label:                pillarConfig.Label,
		Score:                pillarScore,
		Weight:               RoundFloat64(pillarConfig.Weight, 4),
		WeightedContribution: RoundFloat64(float64(pillarScore)*pillarConfig.Weight, 2),
		TotalPenalty:         RoundFloat64(100-float64(pillarScore), 2),
		BucketCount:          int32(len(buckets)),
		IssueTypeCount:       issueTypeCount,
		IssueRowCount:        issueRowCount,
		AffectedURLCount:     int32(len(pillarAffectedURLs)),
		Buckets:              buckets,
	}

}

func resolvePSIBucketScore(bucketID string, psiInput *GooglePSIScoreInput) *float64 {
	if bucketID != "psi_cwv" || psiInput == nil {
		return nil
	}
	if psiInput.MobilePerformanceScore == nil {
		return nil
	}
	score := float64(*psiInput.MobilePerformanceScore)
	return &score
}

// BuildBucketBreakdownWithOptions builds one bucket breakdown from editable scoring config.
func BuildBucketBreakdownWithOptions(bucketID string, bucketWeight float64, issuePenaltyByType map[string]float64, scoringConfig ScoringConfig, totalScoredPages int, issueGroups map[string]*IssueGroup, issueCoverage func(affectedPages int, totalScoredPages int) float64) BucketScoreBreakdown {
	issues := make([]IssueTypeScoreBreakdown, 0, len(issueGroups))
	bucketAffectedURLs := make(map[string]struct{})
	issueRowCount := int32(0)

	for issueType, issueGroup := range issueGroups {
		coverage := issueCoverage(len(issueGroup.AffectedURLs), totalScoredPages)
		basePenalty := IssueBasePenalty(issueType, issuePenaltyByType)
		severityWeight := SeverityMultiplierWithConfig(issueGroup.Severity, scoringConfig)
		finalPenalty := basePenalty * severityWeight * coverage
		issues = append(issues, IssueTypeScoreBreakdown{
			ID:                 issueType,
			Label:              HumanizeIdentifier(issueType),
			Severity:           issueGroup.Severity,
			BasePenalty:        RoundFloat64(basePenalty, 2),
			SeverityMultiplier: RoundFloat64(severityWeight, 2),
			Coverage:           RoundFloat64(coverage, 4),
			FinalPenalty:       RoundFloat64(finalPenalty, 2),
			IssueRowCount:      issueGroup.RowCount,
			AffectedURLCount:   int32(len(issueGroup.AffectedURLs)),
			Message:            issueGroup.Message,
			DetailsPreview:     issueGroup.Details,
		})
		issueRowCount += issueGroup.RowCount
		for affectedURL := range issueGroup.AffectedURLs {
			bucketAffectedURLs[affectedURL] = struct{}{}
		}
	}

	sort.Slice(issues, func(leftIndex int, rightIndex int) bool {
		if issues[leftIndex].FinalPenalty == issues[rightIndex].FinalPenalty {
			return issues[leftIndex].Label < issues[rightIndex].Label
		}
		return issues[leftIndex].FinalPenalty > issues[rightIndex].FinalPenalty
	})

	bucketPenalty := SoftSumPenalties(issues, scoringConfig)
	bucketScore := ClampScore(100-bucketPenalty, 0)
	return BucketScoreBreakdown{
		ID:                   bucketID,
		Label:                HumanizeIdentifier(bucketID),
		Score:                bucketScore,
		Weight:               RoundFloat64(bucketWeight, 4),
		WeightedContribution: RoundFloat64(float64(bucketScore)*bucketWeight, 2),
		TotalPenalty:         RoundFloat64(bucketPenalty, 2),
		IssueTypeCount:       int32(len(issues)),
		IssueRowCount:        issueRowCount,
		AffectedURLCount:     int32(len(bucketAffectedURLs)),
		Issues:               issues,
		}

	}

// IssueCoverage converts the affected page count into a shared saturating proportional coverage score.
func IssueCoverage(affectedPages int, totalScoredPages int) float64 {
	if affectedPages <= 0 || totalScoredPages <= 0 {
		return 0
	}
	coverageRatio := float64(affectedPages) / float64(totalScoredPages)
	return 1 - math.Exp(-coverageRatio*CoverageScale)
}

// IssueCoverageWithConfig converts the affected page count using editable coverage scaling.
func IssueCoverageWithConfig(affectedPages int, totalScoredPages int, scoringConfig ScoringConfig) float64 {
	if affectedPages <= 0 || totalScoredPages <= 0 {
		return 0
	}
	coverageScale := scoringConfig.CoverageScale
	if coverageScale <= 0 {
		coverageScale = CoverageScale
	}
	coverageRatio := float64(affectedPages) / float64(totalScoredPages)
	return 1 - math.Exp(-coverageRatio*coverageScale)
}

// SoftSumPenalties combines per-issue-type bucket penalties with geometric decay so coexisting
// issue types add up without letting a bucket free-fall. issues must already be sorted by
// FinalPenalty descending, so the worst issue counts in full and each lower one decays.
func SoftSumPenalties(issues []IssueTypeScoreBreakdown, scoringConfig ScoringConfig) float64 {
	decay := scoringConfig.SoftSumDecay
	if decay <= 0 || decay >= 1 {
		decay = SoftSumDecay
	}
	bucketPenalty := 0.0
	weight := 1.0
	for issueIndex := range issues {
		bucketPenalty += issues[issueIndex].FinalPenalty * weight
		weight *= decay
	}
	return bucketPenalty
}

// ClampScore rounds one score into the allowed 0-100 range, with an optional minimum.
func ClampScore(score float64, minimum int32) int32 {
	roundedScore := int32(math.Round(score))
	if roundedScore < minimum {
		return minimum
	}
	if roundedScore > 100 {
		return 100
	}
	return roundedScore
}

// RoundFloat64 rounds one float to a fixed number of decimal places.
func RoundFloat64(value float64, decimalPlaces int) float64 {
	multiplier := 1.0
	for decimalPlace := 0; decimalPlace < decimalPlaces; decimalPlace++ {
		multiplier *= 10
	}
	return math.Round(value*multiplier) / multiplier
}
