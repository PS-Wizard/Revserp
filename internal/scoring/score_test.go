package scoring

import "testing"

func TestCalculateScoresUsesPillarScopedIssueBuckets(t *testing.T) {
	crawlPageSignals := []CrawlPageSignal{}
	for pageIndex := 0; pageIndex < 12; pageIndex++ {
		crawlPageSignals = append(crawlPageSignals, CrawlPageSignal{
			URL:         "https://example.com/page-" + string(rune('a'+pageIndex)),
			ContentType: "text/html; charset=utf-8",
		})
	}

	crawlIssueSignals := []CrawlIssueSignal{}
	for pageIndex := 0; pageIndex < 6; pageIndex++ {
		pageURL := crawlPageSignals[pageIndex].URL
		crawlIssueSignals = append(crawlIssueSignals,
			CrawlIssueSignal{URL: pageURL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
			CrawlIssueSignal{URL: pageURL, Pillar: "aeo", Bucket: "answerability", Severity: "high", IssueType: "missing_structured_data"},
			CrawlIssueSignal{URL: pageURL, Pillar: "pagespeed", Bucket: "server_responsiveness", Severity: "high", IssueType: "slow_response_time"},
		)
	}

	scores := CalculateScores(crawlPageSignals, crawlIssueSignals)

	if scores.SEOScore >= 100 || scores.SEOScore <= 0 {
		t.Fatalf("expected seo score to be reduced into range, got %d", scores.SEOScore)
	}
	if scores.AEOScore >= 100 || scores.AEOScore <= 0 {
		t.Fatalf("expected aeo score to be reduced into range, got %d", scores.AEOScore)
	}
	if scores.PageSpeedScore >= 100 || scores.PageSpeedScore <= 0 {
		t.Fatalf("expected pagespeed score to be reduced into range, got %d", scores.PageSpeedScore)
	}
	if scores.OverallScore >= 100 || scores.OverallScore <= 0 {
		t.Fatalf("expected overall score to be reduced into range, got %d", scores.OverallScore)
	}
}

func TestCalculatePillarScoreIgnoresIssuesFromOtherPillars(t *testing.T) {
	crawlPageSignals := []CrawlPageSignal{{URL: "https://example.com/one"}, {URL: "https://example.com/two"}}
	seoScoreWithoutAEOIssues := calculatePillarScore("seo", crawlPageSignals, []CrawlIssueSignal{
		{URL: "https://example.com/one", Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
	})
	seoScoreWithAEOIssues := calculatePillarScore("seo", crawlPageSignals, []CrawlIssueSignal{
		{URL: "https://example.com/one", Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
		{URL: "https://example.com/one", Pillar: "aeo", Bucket: "answerability", Severity: "high", IssueType: "missing_structured_data"},
		{URL: "https://example.com/one", Pillar: "pagespeed", Bucket: "server_responsiveness", Severity: "high", IssueType: "slow_response_time"},
	})

	if seoScoreWithoutAEOIssues != seoScoreWithAEOIssues {
		t.Fatalf("expected other pillars to have no seo effect: %d vs %d", seoScoreWithoutAEOIssues, seoScoreWithAEOIssues)
	}
}

func TestCalculatePillarScoreIgnoresDuplicateIssueURLsPerType(t *testing.T) {
	pageSpeedScoreWithDuplicates := calculatePillarScore("pagespeed", []CrawlPageSignal{{URL: "https://example.com/one"}, {URL: "https://example.com/two"}}, []CrawlIssueSignal{
		{URL: "https://example.com/one", Pillar: "pagespeed", Bucket: "server_responsiveness", Severity: "high", IssueType: "slow_response_time"},
		{URL: "https://example.com/one", Pillar: "pagespeed", Bucket: "server_responsiveness", Severity: "high", IssueType: "slow_response_time"},
	})
	pageSpeedScoreWithoutDuplicates := calculatePillarScore("pagespeed", []CrawlPageSignal{{URL: "https://example.com/one"}, {URL: "https://example.com/two"}}, []CrawlIssueSignal{
		{URL: "https://example.com/one", Pillar: "pagespeed", Bucket: "server_responsiveness", Severity: "high", IssueType: "slow_response_time"},
	})

	if pageSpeedScoreWithDuplicates != pageSpeedScoreWithoutDuplicates {
		t.Fatalf("expected duplicate issue rows to have no extra effect: %d vs %d", pageSpeedScoreWithDuplicates, pageSpeedScoreWithoutDuplicates)
	}
}

func TestCalculateOverallScoreKeepsMinimumOne(t *testing.T) {
	if overallScore := calculateOverallScore(0, 0, 0); overallScore != 1 {
		t.Fatalf("expected minimum overall score of 1, got %d", overallScore)
	}
}
