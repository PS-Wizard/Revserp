package scoring

import (
	"bytes"
	"math"
	"strings"
)

const (
	highSeverityPenalty   = 6.0
	mediumSeverityPenalty = 3.0
	lowSeverityPenalty    = 1.0
	minimumOverallScore   = 1
)

var issuePenaltyByCode = map[string]float64{
	"missing_title":              6,
	"title_too_long":             3,
	"title_too_short":            3,
	"missing_meta_description":   6,
	"meta_description_too_long":  3,
	"meta_description_too_short": 3,
	"missing_h1":                 5,
	"multiple_h1":                5,
	"missing_h2_on_long_page":    4,
	"thin_content":               9,
	"missing_canonical":          4,
	"canonical_differs":          3,
	"missing_viewport":           6,
	"missing_lang":               3,
	"noindex_page":               12,
	"nofollow_page":              6,
	"missing_og_tags":            4,
	"missing_structured_data":    10,
	"images_missing_alt":         4,
	"images_missing_dimensions":  4,
	"slow_response_time":         8,
	"moderate_page_size":         3,
	"large_page_size":            6,
	"low_internal_links_in":      5,
	"no_internal_links_out":      5,
	"low_internal_links_out":     3,
	"client_error_status":        10,
	"server_error_status":        12,
}

// CalculateScores builds the persisted crawl scores from page and issue signals.
func CalculateScores(crawlPageSignals []CrawlPageSignal, crawlIssueSignals []CrawlIssueSignal) CrawlScores {
	seoScore := calculateSEOScore(crawlPageSignals, crawlIssueSignals)
	aeoScore := calculateAEOScore(crawlPageSignals)
	pageSpeedScore := calculatePageSpeedScore(crawlPageSignals)
	overallScore := calculateOverallScore(seoScore, aeoScore, pageSpeedScore)

	return CrawlScores{
		SEOScore:       seoScore,
		AEOScore:       aeoScore,
		PageSpeedScore: pageSpeedScore,
		OverallScore:   overallScore,
	}
}

// calculateSEOScore converts issue coverage into a crawl-level SEO score.
func calculateSEOScore(crawlPageSignals []CrawlPageSignal, crawlIssueSignals []CrawlIssueSignal) int32 {
	totalScoredPages := countScoreablePages(crawlPageSignals)
	if totalScoredPages == 0 {
		return 0
	}

	affectedURLsByIssueCode := make(map[string]map[string]struct{})
	for _, crawlIssueSignal := range crawlIssueSignals {
		if strings.TrimSpace(crawlIssueSignal.URL) == "" {
			continue
		}
		if _, exists := affectedURLsByIssueCode[crawlIssueSignal.Code]; !exists {
			affectedURLsByIssueCode[crawlIssueSignal.Code] = make(map[string]struct{})
		}
		affectedURLsByIssueCode[crawlIssueSignal.Code][crawlIssueSignal.URL] = struct{}{}
	}

	seoScore := 100.0
	for issueCode, affectedURLs := range affectedURLsByIssueCode {
		issuePenalty := issuePenaltyByCode[issueCode]
		if issuePenalty == 0 {
			issuePenalty = fallbackIssuePenalty(issueSeverityForCode(crawlIssueSignals, issueCode))
		}
		affectedPageRatio := float64(len(affectedURLs)) / float64(totalScoredPages)
		seoScore -= issuePenalty * affectedPageRatio
	}

	seoScore = applyBlockerMultiplier(seoScore, affectedURLsByIssueCode, totalScoredPages, "noindex_page", 0.05, 0.70)
	seoScore = applyBlockerMultiplier(seoScore, affectedURLsByIssueCode, totalScoredPages, "missing_title", 0.20, 0.85)
	seoScore = applyBlockerMultiplier(seoScore, affectedURLsByIssueCode, totalScoredPages, "missing_meta_description", 0.40, 0.88)
	seoScore = applyBlockerMultiplier(seoScore, affectedURLsByIssueCode, totalScoredPages, "slow_response_time", 0.20, 0.92)

	return clampScore(seoScore, 0)
}

// calculateAEOScore builds a minimal AEO score from currently persisted page signals.
func calculateAEOScore(crawlPageSignals []CrawlPageSignal) int32 {
	totalScoredPages := countScoreablePages(crawlPageSignals)
	if totalScoredPages == 0 {
		return 0
	}

	httpsCount := 0
	structuredDataCount := 0
	openGraphCount := 0
	contentDepthCount := 0

	for _, crawlPageSignal := range crawlPageSignals {
		if !isScoreablePage(crawlPageSignal) {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(crawlPageSignal.URL)), "https://") {
			httpsCount++
		}
		if hasMeaningfulJSONLD(crawlPageSignal.JSONLD) {
			structuredDataCount++
		}
		if hasMeaningfulOGTags(crawlPageSignal.OGTags) {
			openGraphCount++
		}
		if crawlPageSignal.WordCount >= 300 {
			contentDepthCount++
		}
	}

	weightedSignalScore :=
		1.0*rate(httpsCount, totalScoredPages) +
			1.8*rate(structuredDataCount, totalScoredPages) +
			0.8*rate(openGraphCount, totalScoredPages) +
			1.8*rate(contentDepthCount, totalScoredPages)

	return clampScore((weightedSignalScore/5.4)*100, 0)
}

// calculatePageSpeedScore builds a minimal page speed score from currently persisted page signals.
func calculatePageSpeedScore(crawlPageSignals []CrawlPageSignal) int32 {
	totalScoredPages := countScoreablePages(crawlPageSignals)
	if totalScoredPages == 0 {
		return 0
	}

	responsivenessScore := 0.0
	pageWeightScore := 0.0
	for _, crawlPageSignal := range crawlPageSignals {
		if !isScoreablePage(crawlPageSignal) {
			continue
		}

		if crawlPageSignal.ResponseTimeMs > 0 && crawlPageSignal.ResponseTimeMs <= 1000 {
			responsivenessScore += 1
		}

		switch {
		case crawlPageSignal.SizeBytes == 0:
			pageWeightScore += 1
		case crawlPageSignal.SizeBytes <= 1*1024*1024:
			pageWeightScore += 1
		case crawlPageSignal.SizeBytes <= 3*1024*1024:
			pageWeightScore += 0.5
		}
	}

	weightedSignalScore := 0.55*(responsivenessScore/float64(totalScoredPages)) + 0.45*(pageWeightScore/float64(totalScoredPages))
	return clampScore(weightedSignalScore*100, 0)
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

// issueSeverityForCode returns the first severity associated with one issue code.
func issueSeverityForCode(crawlIssueSignals []CrawlIssueSignal, issueCode string) string {
	for _, crawlIssueSignal := range crawlIssueSignals {
		if crawlIssueSignal.Code == issueCode {
			return crawlIssueSignal.Severity
		}
	}
	return ""
}

// fallbackIssuePenalty maps issue severity to a default scoring penalty.
func fallbackIssuePenalty(issueSeverity string) float64 {
	switch strings.ToLower(strings.TrimSpace(issueSeverity)) {
	case "high":
		return highSeverityPenalty
	case "medium":
		return mediumSeverityPenalty
	case "low":
		return lowSeverityPenalty
	default:
		return mediumSeverityPenalty
	}
}

// applyBlockerMultiplier scales the score down when one issue affects too many pages.
func applyBlockerMultiplier(score float64, affectedURLsByIssueCode map[string]map[string]struct{}, totalScoredPages int, issueCode string, threshold float64, multiplier float64) float64 {
	if totalScoredPages == 0 {
		return score
	}
	affectedURLs := affectedURLsByIssueCode[issueCode]
	if float64(len(affectedURLs))/float64(totalScoredPages) < threshold {
		return score
	}
	return score * multiplier
}

// hasMeaningfulOGTags reports whether persisted og_tags contains any non-empty object data.
func hasMeaningfulOGTags(ogTags []byte) bool {
	trimmedOGTags := bytes.TrimSpace(ogTags)
	if len(trimmedOGTags) == 0 {
		return false
	}
	if bytes.Equal(trimmedOGTags, []byte("null")) {
		return false
	}
	if bytes.Equal(trimmedOGTags, []byte("{}")) {
		return false
	}
	return true
}

// hasMeaningfulJSONLD reports whether persisted json_ld contains any non-empty array data.
func hasMeaningfulJSONLD(jsonLD []byte) bool {
	trimmedJSONLD := bytes.TrimSpace(jsonLD)
	if len(trimmedJSONLD) == 0 {
		return false
	}
	if bytes.Equal(trimmedJSONLD, []byte("null")) {
		return false
	}
	if bytes.Equal(trimmedJSONLD, []byte("[]")) {
		return false
	}
	return true
}

// rate converts a partial count into a 0-1 ratio.
func rate(count int, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
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
