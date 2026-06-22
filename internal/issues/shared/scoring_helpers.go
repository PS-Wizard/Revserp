package shared

import (
	"math"
	"sort"
)

const (
	MinimumOverallScore   = 1
	CoverageScale         = 8.0
	VolumePressureScale   = 1.5
	MaximumVolumePressure = 3.0
)

// BuildPillarBreakdown builds one pillar breakdown from its configured bucket weights and issue penalties.
func BuildPillarBreakdown(pillarID string, pillarLabel string, pillarWeight float64, bucketWeights map[string]float64, issuePenaltyByType map[string]float64, totalScoredPages int, crawlIssueSignals []CrawlIssueSignal) PillarScoreBreakdown {
	return BuildPillarBreakdownWithIssueCoverage(pillarID, pillarLabel, pillarWeight, bucketWeights, issuePenaltyByType, totalScoredPages, crawlIssueSignals, IssueCoverage)
}

// BuildPillarBreakdownWithIssueCoverage builds one pillar breakdown using a pillar-specific issue coverage function.
func BuildPillarBreakdownWithIssueCoverage(pillarID string, pillarLabel string, pillarWeight float64, bucketWeights map[string]float64, issuePenaltyByType map[string]float64, totalScoredPages int, crawlIssueSignals []CrawlIssueSignal, issueCoverage func(affectedPages int, totalScoredPages int) float64) PillarScoreBreakdown {
	return BuildPillarBreakdownWithOptions(pillarID, PillarScoringConfig{Label: pillarLabel, Weight: pillarWeight, BucketWeights: bucketWeights, IssuePenaltyByType: issuePenaltyByType}, DefaultScoringMathConfig(), totalScoredPages, crawlIssueSignals, issueCoverage, nil)
}

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

	volumeMultiplier := IssueVolumeMultiplierWithConfig(issueRowCount, totalScoredPages, scoringConfig)
	weightedBucketScoreSum := 0.0
	for bucketIndex := range buckets {
		psiScore := resolvePSIBucketScore(buckets[bucketIndex].ID, psiInput)
		adjustedBucketPenalty := buckets[bucketIndex].TotalPenalty * volumeMultiplier
		adjustedBucketScore := ClampScore(100-adjustedBucketPenalty, 0)
		if psiScore != nil {
			adjustedBucketScore = ClampScore(*psiScore, 0)
			adjustedBucketPenalty = float64(100 - adjustedBucketScore)
		}
		buckets[bucketIndex].Score = adjustedBucketScore
		buckets[bucketIndex].WeightedContribution = RoundFloat64(float64(adjustedBucketScore)*buckets[bucketIndex].Weight, 2)
		buckets[bucketIndex].TotalPenalty = RoundFloat64(adjustedBucketPenalty, 2)
		weightedBucketScoreSum += float64(adjustedBucketScore) * buckets[bucketIndex].Weight
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

// BuildBucketBreakdown builds one bucket breakdown from its configured issue groups.
func BuildBucketBreakdown(bucketID string, bucketWeight float64, issuePenaltyByType map[string]float64, totalScoredPages int, issueGroups map[string]*IssueGroup) BucketScoreBreakdown {
	return BuildBucketBreakdownWithIssueCoverage(bucketID, bucketWeight, issuePenaltyByType, totalScoredPages, issueGroups, IssueCoverage)
}

// BuildBucketBreakdownWithIssueCoverage builds one bucket breakdown using a pillar-specific issue coverage function.
func BuildBucketBreakdownWithIssueCoverage(bucketID string, bucketWeight float64, issuePenaltyByType map[string]float64, totalScoredPages int, issueGroups map[string]*IssueGroup, issueCoverage func(affectedPages int, totalScoredPages int) float64) BucketScoreBreakdown {
	return BuildBucketBreakdownWithOptions(bucketID, bucketWeight, issuePenaltyByType, DefaultScoringMathConfig(), totalScoredPages, issueGroups, issueCoverage)
}

// BuildBucketBreakdownWithOptions builds one bucket breakdown from editable scoring config.
func BuildBucketBreakdownWithOptions(bucketID string, bucketWeight float64, issuePenaltyByType map[string]float64, scoringConfig ScoringConfig, totalScoredPages int, issueGroups map[string]*IssueGroup, issueCoverage func(affectedPages int, totalScoredPages int) float64) BucketScoreBreakdown {
	issues := make([]IssueTypeScoreBreakdown, 0, len(issueGroups))
	bucketPenalty := 0.0
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
		bucketPenalty += finalPenalty
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

// IssueVolumeMultiplier returns an extra pillar-level penalty multiplier from issue rows per scoreable page.
func IssueVolumeMultiplier(issueRowCount int32, totalScoredPages int) float64 {
	if issueRowCount <= 0 || totalScoredPages <= 0 {
		return 1
	}
	issueRowsPerPage := float64(issueRowCount) / float64(totalScoredPages)
	extraPressure := math.Log1p(issueRowsPerPage) * VolumePressureScale
	if extraPressure > MaximumVolumePressure {
		extraPressure = MaximumVolumePressure
	}
	return 1 + extraPressure
}

// IssueVolumeMultiplierWithConfig returns an editable issue-volume pressure multiplier.
func IssueVolumeMultiplierWithConfig(issueRowCount int32, totalScoredPages int, scoringConfig ScoringConfig) float64 {
	if issueRowCount <= 0 || totalScoredPages <= 0 {
		return 1
	}
	volumePressureScale := scoringConfig.VolumePressureScale
	if volumePressureScale <= 0 {
		volumePressureScale = VolumePressureScale
	}
	maximumVolumePressure := scoringConfig.MaximumVolumePressure
	if maximumVolumePressure <= 0 {
		maximumVolumePressure = MaximumVolumePressure
	}
	issueRowsPerPage := float64(issueRowCount) / float64(totalScoredPages)
	extraPressure := math.Log1p(issueRowsPerPage) * volumePressureScale
	if extraPressure > maximumVolumePressure {
		extraPressure = maximumVolumePressure
	}
	return 1 + extraPressure
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
