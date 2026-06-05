package scoring

import (
	"math"
	"strings"
)

const (
	minimumOverallScore = 1
	defaultIssuePenalty = 6.0
	coverageScale       = 8.0
)

var pillarBucketWeights = map[string]map[string]float64{
	"seo": {
		"serp_metadata":      0.18,
		"content_structure":  0.16,
		"content_quality":    0.20,
		"indexability":       0.20,
		"technical_seo":      0.08,
		"media_optimization": 0.06,
		"internal_linking":   0.12,
	},
	"aeo": {
		"experience":        0.15,
		"expertise":         0.20,
		"authoritativeness": 0.20,
		"trust":             0.20,
		"answerability":     0.25,
	},
	"pagespeed": {
		"server_responsiveness": 0.55,
		"page_weight":           0.45,
		"psi_cwv":               0.00,
	},
}

var issuePenaltyByType = map[string]float64{
	"missing_title":              12,
	"title_too_long":             5,
	"title_too_short":            5,
	"missing_meta_description":   10,
	"meta_description_too_long":  4,
	"meta_description_too_short": 4,
	"missing_h1":                 10,
	"multiple_h1":                7,
	"missing_h2_on_long_page":    6,
	"skipped_heading_levels":     6,
	"thin_content":               12,
	"exact_duplicate_content":    12,
	"near_duplicate_content":     8,
	"missing_canonical":          8,
	"canonical_differs":          5,
	"noindex_page":               14,
	"nofollow_page":              6,
	"missing_viewport":           8,
	"missing_lang":               4,
	"client_error_status":        12,
	"server_error_status":        14,
	"images_missing_alt":         5,
	"images_missing_dimensions":  4,
	"too_many_images_on_page":    4,
	"low_internal_links_in":      8,
	"low_internal_links_out":     5,
	"no_internal_links_out":      8,
	"missing_author_signal":      8,
	"missing_external_citations": 7,
	"missing_https":              10,
	"missing_og_tags":            4,
	"missing_structured_data":    10,
	"slow_response_time":         12,
	"moderate_page_size":         5,
	"large_page_size":            10,
}

var severityMultiplierByLevel = map[string]float64{
	"high":   1.00,
	"medium": 0.60,
	"low":    0.30,
}

// CalculateScores builds the persisted crawl scores from issue-derived pillar scoring.
func CalculateScores(crawlPageSignals []CrawlPageSignal, crawlIssueSignals []CrawlIssueSignal) CrawlScores {
	seoScore := calculatePillarScore("seo", crawlPageSignals, crawlIssueSignals)
	aeoScore := calculatePillarScore("aeo", crawlPageSignals, crawlIssueSignals)
	pageSpeedScore := calculatePillarScore("pagespeed", crawlPageSignals, crawlIssueSignals)
	overallScore := calculateOverallScore(seoScore, aeoScore, pageSpeedScore)

	return CrawlScores{
		SEOScore:       seoScore,
		AEOScore:       aeoScore,
		PageSpeedScore: pageSpeedScore,
		OverallScore:   overallScore,
	}
}

// calculatePillarScore builds one 0-100 pillar score from weighted bucket scores.
func calculatePillarScore(pillar string, crawlPageSignals []CrawlPageSignal, crawlIssueSignals []CrawlIssueSignal) int32 {
	totalScoredPages := countScoreablePages(crawlPageSignals)
	if totalScoredPages == 0 {
		return 0
	}

	bucketWeights, exists := pillarBucketWeights[pillar]
	if !exists {
		return 0
	}

	issueGroupsByBucket := buildIssueGroupsByBucket(pillar, crawlIssueSignals)
	weightedBucketScoreSum := 0.0
	for bucket, bucketWeight := range bucketWeights {
		bucketScore := 100.0 - calculateBucketPenalty(issueGroupsByBucket[bucket])
		weightedBucketScoreSum += clampBucketScore(bucketScore) * bucketWeight
	}

	return clampScore(weightedBucketScoreSum, 0)
}

// buildIssueGroupsByBucket groups unique affected URLs and max severity per issue type inside one pillar bucket.
func buildIssueGroupsByBucket(pillar string, crawlIssueSignals []CrawlIssueSignal) map[string]map[string]*issueGroup {
	issueGroupsByBucket := make(map[string]map[string]*issueGroup)
	for _, crawlIssueSignal := range crawlIssueSignals {
		if strings.TrimSpace(crawlIssueSignal.URL) == "" || crawlIssueSignal.Pillar != pillar || strings.TrimSpace(crawlIssueSignal.Bucket) == "" {
			continue
		}
		if _, exists := issueGroupsByBucket[crawlIssueSignal.Bucket]; !exists {
			issueGroupsByBucket[crawlIssueSignal.Bucket] = make(map[string]*issueGroup)
		}
		if _, exists := issueGroupsByBucket[crawlIssueSignal.Bucket][crawlIssueSignal.IssueType]; !exists {
			issueGroupsByBucket[crawlIssueSignal.Bucket][crawlIssueSignal.IssueType] = &issueGroup{affectedURLs: make(map[string]struct{})}
		}

		issueGroup := issueGroupsByBucket[crawlIssueSignal.Bucket][crawlIssueSignal.IssueType]
		previousSeverity := issueGroup.severity
		issueGroup.affectedURLs[crawlIssueSignal.URL] = struct{}{}
		issueGroup.rowCount++
		issueGroup.severity = maxSeverity(issueGroup.severity, crawlIssueSignal.Severity)
		if severityRank(issueGroup.severity) > severityRank(previousSeverity) {
			issueGroup.message = crawlIssueSignal.Message
			issueGroup.details = crawlIssueSignal.Details
		}
		if strings.TrimSpace(issueGroup.message) == "" {
			issueGroup.message = crawlIssueSignal.Message
		}
		if strings.TrimSpace(issueGroup.details) == "" {
			issueGroup.details = crawlIssueSignal.Details
		}
	}

	return issueGroupsByBucket
}

// calculateBucketPenalty sums issue penalties for one bucket.
func calculateBucketPenalty(issueGroups map[string]*issueGroup) float64 {
	bucketPenalty := 0.0
	for issueType, issueGroup := range issueGroups {
		bucketPenalty += issueBasePenalty(issueType) * severityMultiplier(issueGroup.severity) * issueCoverage(len(issueGroup.affectedURLs))
	}

	return bucketPenalty
}

// calculateOverallScore combines the top-level crawl scores into one overall score.
func calculateOverallScore(seoScore int32, aeoScore int32, pageSpeedScore int32) int32 {
	overallScore := 0.65*float64(seoScore) + 0.20*float64(aeoScore) + 0.15*float64(pageSpeedScore)
	return clampScore(overallScore, minimumOverallScore)
}

// countScoreablePages returns the number of HTML-like pages eligible for crawl scoring.
func countScoreablePages(crawlPageSignals []CrawlPageSignal) int {
	totalScoredPages := 0
	for _, crawlPageSignal := range crawlPageSignals {
		if isScoreablePage(crawlPageSignal) {
			totalScoredPages++
		}
	}
	return totalScoredPages
}

// isScoreablePage reports whether the page should count toward crawl-level scoring.
func isScoreablePage(crawlPageSignal CrawlPageSignal) bool {
	contentType := strings.ToLower(strings.TrimSpace(crawlPageSignal.ContentType))
	if contentType == "" {
		return true
	}
	return strings.Contains(contentType, "text/html")
}

type issueGroup struct {
	affectedURLs map[string]struct{}
	severity     string
	rowCount     int32
	message      string
	details      string
}

// issueBasePenalty returns the configured base penalty for one issue type.
func issueBasePenalty(issueType string) float64 {
	if penalty, exists := issuePenaltyByType[issueType]; exists {
		return penalty
	}

	return defaultIssuePenalty
}

// severityMultiplier returns the configured penalty multiplier for one severity level.
func severityMultiplier(severity string) float64 {
	if multiplier, exists := severityMultiplierByLevel[strings.ToLower(strings.TrimSpace(severity))]; exists {
		return multiplier
	}

	return severityMultiplierByLevel["medium"]
}

// issueCoverage converts the affected page count into a shared saturating coverage score.
func issueCoverage(affectedPages int) float64 {
	if affectedPages <= 0 {
		return 0
	}

	return 1 - math.Exp(-float64(affectedPages)/coverageScale)
}

// maxSeverity returns the stronger of two severities.
func maxSeverity(currentSeverity string, candidateSeverity string) string {
	if severityRank(candidateSeverity) > severityRank(currentSeverity) {
		return candidateSeverity
	}

	return currentSeverity
}

// severityRank orders severities from weakest to strongest.
func severityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// clampBucketScore bounds one bucket score to the allowed 0-100 range.
func clampBucketScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// clampScore rounds one score into the allowed 0-100 range, with an optional minimum.
func clampScore(score float64, minimum int32) int32 {
	roundedScore := int32(math.Round(score))
	if roundedScore < minimum {
		return minimum
	}
	if roundedScore > 100 {
		return 100
	}
	return roundedScore
}
