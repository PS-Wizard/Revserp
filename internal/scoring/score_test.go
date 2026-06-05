package scoring

import "testing"

func TestCalculateScores(t *testing.T) {
	scores := CalculateScores([]CrawlPageSignal{
		{
			URL:            "https://example.com/healthy",
			ContentType:    "text/html; charset=utf-8",
			WordCount:      450,
			ResponseTimeMs: 200,
			SizeBytes:      300 * 1024,
			OGTags:         []byte(`{"og:title":"Healthy"}`),
			JSONLD:         []byte(`[{"@type":"Article"}]`),
		},
		{
			URL:            "https://example.com/problem",
			ContentType:    "text/html; charset=utf-8",
			WordCount:      80,
			ResponseTimeMs: 1800,
			SizeBytes:      4 * 1024 * 1024,
		},
	}, []CrawlIssueSignal{
		{URL: "https://example.com/problem", Severity: "high", IssueType: "missing_title"},
		{URL: "https://example.com/problem", Severity: "medium", IssueType: "thin_content"},
		{URL: "https://example.com/problem", Severity: "medium", IssueType: "slow_response_time"},
		{URL: "https://example.com/problem", Severity: "high", IssueType: "large_page_size"},
		{URL: "https://example.com/problem", Severity: "high", IssueType: "missing_structured_data"},
	})

	if scores.SEOScore <= 0 || scores.SEOScore >= 100 {
		t.Fatalf("unexpected seo score %d", scores.SEOScore)
	}
	if scores.AEOScore <= 0 || scores.AEOScore >= 100 {
		t.Fatalf("unexpected aeo score %d", scores.AEOScore)
	}
	if scores.PageSpeedScore <= 0 || scores.PageSpeedScore >= 100 {
		t.Fatalf("unexpected pagespeed score %d", scores.PageSpeedScore)
	}
	if scores.OverallScore <= 0 || scores.OverallScore >= 100 {
		t.Fatalf("unexpected overall score %d", scores.OverallScore)
	}
}

func TestCalculateScoresIgnoresDuplicateIssueURLsPerCode(t *testing.T) {
	seoScoreWithDuplicates := calculateSEOScore([]CrawlPageSignal{{URL: "https://example.com/one"}, {URL: "https://example.com/two"}}, []CrawlIssueSignal{
		{URL: "https://example.com/one", Severity: "high", IssueType: "missing_title"},
		{URL: "https://example.com/one", Severity: "high", IssueType: "missing_title"},
	})
	seoScoreWithoutDuplicates := calculateSEOScore([]CrawlPageSignal{{URL: "https://example.com/one"}, {URL: "https://example.com/two"}}, []CrawlIssueSignal{
		{URL: "https://example.com/one", Severity: "high", IssueType: "missing_title"},
	})

	if seoScoreWithDuplicates != seoScoreWithoutDuplicates {
		t.Fatalf("expected duplicate issue rows to have no extra effect: %d vs %d", seoScoreWithDuplicates, seoScoreWithoutDuplicates)
	}
}

func TestCalculateOverallScoreKeepsMinimumOne(t *testing.T) {
	if overallScore := calculateOverallScore(0, 0, 0); overallScore != 1 {
		t.Fatalf("expected minimum overall score of 1, got %d", overallScore)
	}
}
