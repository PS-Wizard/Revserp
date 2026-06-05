package scoring

import (
	"math"
	"sort"
	"strings"
)

const scoringVersion = "v1"

var pillarWeights = map[string]float64{
	"seo":       0.65,
	"aeo":       0.20,
	"pagespeed": 0.15,
}

var pillarOrder = []string{"aeo", "seo", "pagespeed"}

// ScoreBreakdownSnapshot holds one persisted crawl scoring explanation snapshot.
type ScoreBreakdownSnapshot struct {
	CrawlID          string                 `json:"crawl_id"`
	ScoringVersion   string                 `json:"scoring_version"`
	CoverageScale    float64                `json:"coverage_scale"`
	TotalScoredPages int32                  `json:"total_scored_pages"`
	OverallScore     int32                  `json:"overall_score"`
	Pillars          []PillarScoreBreakdown `json:"pillars"`
}

// CrawlScores converts one snapshot back into the persisted top-level crawl scores.
func (snapshot ScoreBreakdownSnapshot) CrawlScores() CrawlScores {
	crawlScores := CrawlScores{OverallScore: snapshot.OverallScore}
	for _, pillar := range snapshot.Pillars {
		switch pillar.ID {
		case "seo":
			crawlScores.SEOScore = pillar.Score
		case "aeo":
			crawlScores.AEOScore = pillar.Score
		case "pagespeed":
			crawlScores.PageSpeedScore = pillar.Score
		}
	}

	return crawlScores
}

// PillarScoreBreakdown explains one pillar's score and weighted buckets.
type PillarScoreBreakdown struct {
	ID                   string                 `json:"id"`
	Label                string                 `json:"label"`
	Score                int32                  `json:"score"`
	Weight               float64                `json:"weight"`
	WeightedContribution float64                `json:"weighted_contribution"`
	TotalPenalty         float64                `json:"total_penalty"`
	BucketCount          int32                  `json:"bucket_count"`
	IssueTypeCount       int32                  `json:"issue_type_count"`
	IssueRowCount        int32                  `json:"issue_row_count"`
	AffectedURLCount     int32                  `json:"affected_url_count"`
	Buckets              []BucketScoreBreakdown `json:"buckets"`
}

// BucketScoreBreakdown explains one weighted bucket inside a pillar.
type BucketScoreBreakdown struct {
	ID                   string                    `json:"id"`
	Label                string                    `json:"label"`
	Score                int32                     `json:"score"`
	Weight               float64                   `json:"weight"`
	WeightedContribution float64                   `json:"weighted_contribution"`
	TotalPenalty         float64                   `json:"total_penalty"`
	IssueTypeCount       int32                     `json:"issue_type_count"`
	IssueRowCount        int32                     `json:"issue_row_count"`
	AffectedURLCount     int32                     `json:"affected_url_count"`
	Issues               []IssueTypeScoreBreakdown `json:"issues"`
}

// IssueTypeScoreBreakdown explains one grouped issue type inside a bucket.
type IssueTypeScoreBreakdown struct {
	ID                 string  `json:"id"`
	Label              string  `json:"label"`
	Severity           string  `json:"severity"`
	BasePenalty        float64 `json:"base_penalty"`
	SeverityMultiplier float64 `json:"severity_multiplier"`
	Coverage           float64 `json:"coverage"`
	FinalPenalty       float64 `json:"final_penalty"`
	IssueRowCount      int32   `json:"issue_row_count"`
	AffectedURLCount   int32   `json:"affected_url_count"`
	Message            string  `json:"message"`
	DetailsPreview     string  `json:"details_preview"`
}

// BuildScoreBreakdown builds one persisted crawl score snapshot from current issue signals.
func BuildScoreBreakdown(crawlID string, crawlPageSignals []CrawlPageSignal, crawlIssueSignals []CrawlIssueSignal) ScoreBreakdownSnapshot {
	totalScoredPages := countScoreablePages(crawlPageSignals)
	pillars := make([]PillarScoreBreakdown, 0, len(pillarOrder))
	if totalScoredPages == 0 {
		for _, pillarID := range pillarOrder {
			pillars = append(pillars, newEmptyPillarBreakdown(pillarID))
		}
		return ScoreBreakdownSnapshot{
			CrawlID:          crawlID,
			ScoringVersion:   scoringVersion,
			CoverageScale:    coverageScale,
			TotalScoredPages: 0,
			OverallScore:     0,
			Pillars:          pillars,
		}
	}

	for _, pillarID := range pillarOrder {
		pillars = append(pillars, buildPillarBreakdown(pillarID, crawlIssueSignals))
	}

	snapshot := ScoreBreakdownSnapshot{
		CrawlID:          crawlID,
		ScoringVersion:   scoringVersion,
		CoverageScale:    coverageScale,
		TotalScoredPages: int32(totalScoredPages),
		Pillars:          pillars,
	}
	snapshot.OverallScore = calculateOverallScore(snapshot.CrawlScores().SEOScore, snapshot.CrawlScores().AEOScore, snapshot.CrawlScores().PageSpeedScore)
	return snapshot
}

func newEmptyPillarBreakdown(pillarID string) PillarScoreBreakdown {
	bucketWeights := pillarBucketWeights[pillarID]
	buckets := make([]BucketScoreBreakdown, 0, len(bucketWeights))
	for _, bucketID := range sortedBucketIDs(bucketWeights) {
		buckets = append(buckets, BucketScoreBreakdown{
			ID:                   bucketID,
			Label:                labelForBucket(bucketID),
			Score:                0,
			Weight:               roundFloat64(bucketWeights[bucketID], 4),
			WeightedContribution: 0,
			TotalPenalty:         0,
		})
	}

	return PillarScoreBreakdown{
		ID:                   pillarID,
		Label:                labelForPillar(pillarID),
		Score:                0,
		Weight:               roundFloat64(pillarWeights[pillarID], 4),
		WeightedContribution: 0,
		TotalPenalty:         0,
		BucketCount:          int32(len(buckets)),
		Buckets:              buckets,
	}
}

func buildPillarBreakdown(pillarID string, crawlIssueSignals []CrawlIssueSignal) PillarScoreBreakdown {
	bucketWeights := pillarBucketWeights[pillarID]
	issueGroupsByBucket := buildIssueGroupsByBucket(pillarID, crawlIssueSignals)
	buckets := make([]BucketScoreBreakdown, 0, len(bucketWeights))
	weightedBucketScoreSum := 0.0
	pillarAffectedURLs := make(map[string]struct{})
	issueTypeCount := int32(0)
	issueRowCount := int32(0)

	for _, bucketID := range sortedBucketIDs(bucketWeights) {
		bucketBreakdown := buildBucketBreakdown(bucketID, bucketWeights[bucketID], issueGroupsByBucket[bucketID])
		buckets = append(buckets, bucketBreakdown)
		weightedBucketScoreSum += float64(bucketBreakdown.Score) * bucketBreakdown.Weight
		issueTypeCount += bucketBreakdown.IssueTypeCount
		issueRowCount += bucketBreakdown.IssueRowCount
		for _, issueGroup := range issueGroupsByBucket[bucketID] {
			for affectedURL := range issueGroup.affectedURLs {
				pillarAffectedURLs[affectedURL] = struct{}{}
			}
		}
	}

	pillarScore := clampScore(weightedBucketScoreSum, 0)
	return PillarScoreBreakdown{
		ID:                   pillarID,
		Label:                labelForPillar(pillarID),
		Score:                pillarScore,
		Weight:               roundFloat64(pillarWeights[pillarID], 4),
		WeightedContribution: roundFloat64(float64(pillarScore)*pillarWeights[pillarID], 2),
		TotalPenalty:         roundFloat64(100-float64(pillarScore), 2),
		BucketCount:          int32(len(buckets)),
		IssueTypeCount:       issueTypeCount,
		IssueRowCount:        issueRowCount,
		AffectedURLCount:     int32(len(pillarAffectedURLs)),
		Buckets:              buckets,
	}
}

func buildBucketBreakdown(bucketID string, bucketWeight float64, issueGroups map[string]*issueGroup) BucketScoreBreakdown {
	issues := make([]IssueTypeScoreBreakdown, 0, len(issueGroups))
	bucketPenalty := 0.0
	bucketAffectedURLs := make(map[string]struct{})
	issueRowCount := int32(0)

	for issueType, issueGroup := range issueGroups {
		coverage := issueCoverage(len(issueGroup.affectedURLs))
		basePenalty := issueBasePenalty(issueType)
		severityWeight := severityMultiplier(issueGroup.severity)
		finalPenalty := basePenalty * severityWeight * coverage
		issues = append(issues, IssueTypeScoreBreakdown{
			ID:                 issueType,
			Label:              labelForIssueType(issueType),
			Severity:           issueGroup.severity,
			BasePenalty:        roundFloat64(basePenalty, 2),
			SeverityMultiplier: roundFloat64(severityWeight, 2),
			Coverage:           roundFloat64(coverage, 4),
			FinalPenalty:       roundFloat64(finalPenalty, 2),
			IssueRowCount:      issueGroup.rowCount,
			AffectedURLCount:   int32(len(issueGroup.affectedURLs)),
			Message:            issueGroup.message,
			DetailsPreview:     issueGroup.details,
		})
		bucketPenalty += finalPenalty
		issueRowCount += issueGroup.rowCount
		for affectedURL := range issueGroup.affectedURLs {
			bucketAffectedURLs[affectedURL] = struct{}{}
		}
	}

	sort.Slice(issues, func(leftIndex int, rightIndex int) bool {
		if issues[leftIndex].FinalPenalty == issues[rightIndex].FinalPenalty {
			return issues[leftIndex].Label < issues[rightIndex].Label
		}
		return issues[leftIndex].FinalPenalty > issues[rightIndex].FinalPenalty
	})

	bucketScore := clampScore(100-bucketPenalty, 0)
	return BucketScoreBreakdown{
		ID:                   bucketID,
		Label:                labelForBucket(bucketID),
		Score:                bucketScore,
		Weight:               roundFloat64(bucketWeight, 4),
		WeightedContribution: roundFloat64(float64(bucketScore)*bucketWeight, 2),
		TotalPenalty:         roundFloat64(bucketPenalty, 2),
		IssueTypeCount:       int32(len(issues)),
		IssueRowCount:        issueRowCount,
		AffectedURLCount:     int32(len(bucketAffectedURLs)),
		Issues:               issues,
	}
}

func sortedBucketIDs(bucketWeights map[string]float64) []string {
	bucketIDs := make([]string, 0, len(bucketWeights))
	for bucketID := range bucketWeights {
		bucketIDs = append(bucketIDs, bucketID)
	}

	sort.Slice(bucketIDs, func(leftIndex int, rightIndex int) bool {
		leftBucketID := bucketIDs[leftIndex]
		rightBucketID := bucketIDs[rightIndex]
		if bucketWeights[leftBucketID] == bucketWeights[rightBucketID] {
			return labelForBucket(leftBucketID) < labelForBucket(rightBucketID)
		}
		return bucketWeights[leftBucketID] > bucketWeights[rightBucketID]
	})

	return bucketIDs
}

func labelForPillar(pillarID string) string {
	switch pillarID {
	case "seo":
		return "SEO"
	case "aeo":
		return "AEO"
	case "pagespeed":
		return "PageSpeed"
	default:
		return humanizeIdentifier(pillarID)
	}
}

func labelForBucket(bucketID string) string {
	return humanizeIdentifier(bucketID)
}

func labelForIssueType(issueType string) string {
	return humanizeIdentifier(issueType)
}

func humanizeIdentifier(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "_")
	for partIndex, part := range parts {
		switch strings.ToLower(part) {
		case "seo":
			parts[partIndex] = "SEO"
		case "aeo":
			parts[partIndex] = "AEO"
		case "og":
			parts[partIndex] = "OG"
		case "psi":
			parts[partIndex] = "PSI"
		case "h1", "h2", "h3":
			parts[partIndex] = strings.ToUpper(part)
		default:
			if part == "" {
				continue
			}
			parts[partIndex] = strings.ToUpper(part[:1]) + part[1:]
		}
	}

	return strings.Join(parts, " ")
}

func roundFloat64(value float64, decimalPlaces int) float64 {
	multiplier := 1.0
	for decimalPlace := 0; decimalPlace < decimalPlaces; decimalPlace++ {
		multiplier *= 10
	}
	return math.Round(value*multiplier) / multiplier
}
