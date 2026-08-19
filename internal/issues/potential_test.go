package issues

import (
	"fmt"
	"testing"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func potentialTestPages() []shared.CrawlPageSignal {
	pages := make([]shared.CrawlPageSignal, 0, 10)
	for i := 0; i < 10; i++ {
		pages = append(pages, shared.CrawlPageSignal{
			URL:         fmt.Sprintf("https://example.com/p%d", i),
			StatusCode:  200,
			ContentType: "text/html",
			WordCount:   500,
		})
	}
	return pages
}

func potentialTestIssues() []shared.CrawlIssueSignal {
	issues := make([]shared.CrawlIssueSignal, 0, 20)
	for i := 0; i < 10; i++ {
		issues = append(issues, shared.CrawlIssueSignal{URL: fmt.Sprintf("https://example.com/p%d", i), Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title", Severity: "high"})
	}
	for i := 0; i < 4; i++ {
		issues = append(issues, shared.CrawlIssueSignal{URL: fmt.Sprintf("https://example.com/p%d", i), Pillar: "aeo", Bucket: "answerability", IssueType: "missing_citations", Severity: "medium"})
	}
	for i := 0; i < 5; i++ {
		issues = append(issues, shared.CrawlIssueSignal{URL: fmt.Sprintf("https://example.com/p%d", i), Pillar: "pagespeed", Bucket: "server_responsiveness", IssueType: "slow_response_time", Severity: "medium"})
	}
	issues = append(issues, shared.CrawlIssueSignal{URL: "https://example.com/p0", Pillar: "pagespeed", Bucket: psiCoreWebVitalsBucket, IssueType: "google_psi_mobile_performance", Severity: "high"})
	return issues
}

func potentialTestInput() ([]shared.CrawlPageSignal, []shared.CrawlIssueSignal, shared.ScoringConfig, *shared.GooglePSIScoreInput) {
	psi := 85
	return potentialTestPages(), potentialTestIssues(), DefaultScoringConfig(), &shared.GooglePSIScoreInput{MobilePerformanceScore: &psi}
}

func directScores(pages []shared.CrawlPageSignal, issues []shared.CrawlIssueSignal, config shared.ScoringConfig, psi *shared.GooglePSIScoreInput) Scores {
	return ComputePotential(pages, issues, config, psi).Current
}

func TestPotentialBaselineMatchesScorer(t *testing.T) {
	pages, issues, config, psi := potentialTestInput()
	result := ComputePotential(pages, issues, config, psi)

	breakdown := BuildScoreBreakdownWithConfig("crawl", pages, issues, config, psi)
	crawlScores := breakdown.CrawlScores()
	if result.Current.Overall != crawlScores.OverallScore ||
		result.Current.SEO != crawlScores.SEOScore ||
		result.Current.AEO != crawlScores.AEOScore ||
		result.Current.PageSpeed != crawlScores.PageSpeedScore {
		t.Fatalf("baseline = %+v, want scorer output %+v", result.Current, crawlScores)
	}
}

func TestPotentialBucketCountAndRanking(t *testing.T) {
	pages, issues, config, psi := potentialTestInput()
	result := ComputePotential(pages, issues, config, psi)

	if len(result.Opportunities) != 15 {
		t.Fatalf("opportunities = %d buckets, want 15", len(result.Opportunities))
	}
	for i := 1; i < len(result.Opportunities); i++ {
		if result.Opportunities[i-1].Delta.Overall < result.Opportunities[i].Delta.Overall {
			t.Fatalf("opportunities not ranked by overall gain at %d: %d < %d",
				i, result.Opportunities[i-1].Delta.Overall, result.Opportunities[i].Delta.Overall)
		}
	}
}

func TestPotentialEmptyBucketHasZeroDelta(t *testing.T) {
	pages, issues, config, psi := potentialTestInput()
	result := ComputePotential(pages, issues, config, psi)

	used := map[string]bool{}
	for _, issue := range issues {
		used[issue.Bucket] = true
	}
	emptyBucket := ""
	for _, opportunity := range result.Opportunities {
		if !used[opportunity.Bucket] {
			emptyBucket = opportunity.Bucket
			break
		}
	}
	if emptyBucket == "" {
		t.Fatal("expected at least one bucket without issues in the fixture")
	}
	for _, opportunity := range result.Opportunities {
		if opportunity.Bucket == emptyBucket && opportunity.Delta.Overall != 0 {
			t.Fatalf("empty bucket %s delta = %d, want 0", emptyBucket, opportunity.Delta.Overall)
		}
	}
}

func TestPotentialPSIBucketUsesPerfectScore(t *testing.T) {
	pages, issues, config, psi := potentialTestInput()
	result := ComputePotential(pages, issues, config, psi)

	var cwv *BucketPotential
	for i := range result.Opportunities {
		if result.Opportunities[i].Bucket == psiCoreWebVitalsBucket {
			cwv = &result.Opportunities[i]
			break
		}
	}
	if cwv == nil {
		t.Fatal("psi_cwv bucket missing from opportunities")
	}

	// Fixing psi_cwv must lift PageSpeed by moving the stored PSI score to 100
	// (85 -> 100 at the psi_cwv weight), not by removing its issue rows alone.
	perfect := 100
	withoutCWVIssues := withoutBucket(issues, psiCoreWebVitalsBucket)
	direct := BuildScoreBreakdownWithConfig("crawl", pages, withoutCWVIssues, config, &shared.GooglePSIScoreInput{MobilePerformanceScore: &perfect}).CrawlScores()
	if cwv.Scores.PageSpeed != direct.PageSpeedScore || cwv.Scores.Overall != direct.OverallScore {
		t.Fatalf("psi_cwv fixed = %+v, want direct rerun %+v", cwv.Scores, direct)
	}
	if cwv.Delta.PageSpeed <= 0 {
		t.Fatalf("psi_cwv fixed PageSpeed delta = %d, want positive (stored PSI 85 fixed to 100)", cwv.Delta.PageSpeed)
	}

	// The baseline must be scored with the real stored PSI input, not the
	// hypothetical perfect one.
	if result.Current.PageSpeed == 100 {
		t.Fatalf("baseline PageSpeed = 100, want it to reflect the stored PSI score 85")
	}
}

func TestPotentialScenariosAreCombinedReruns(t *testing.T) {
	pages, issues, config, psi := potentialTestInput()
	result := ComputePotential(pages, issues, config, psi)

	assertScenarioMatchesDirectRun(t, pages, issues, config, psi, result.Best, "best")
	assertScenarioMatchesDirectRun(t, pages, issues, config, psi, result.Top3, "top_3")
	assertScenarioMatchesDirectRun(t, pages, issues, config, psi, result.Recommended, "recommended")

	if len(result.Best.Buckets) != 1 {
		t.Fatalf("best bucket scenario = %v, want exactly 1 bucket", result.Best.Buckets)
	}
	if len(result.Top3.Buckets) != 3 {
		t.Fatalf("top 3 scenario = %v, want 3 buckets", result.Top3.Buckets)
	}
	if result.Best.Buckets[0] != result.Opportunities[0].Bucket {
		t.Fatalf("best bucket %q != top opportunity %q", result.Best.Buckets[0], result.Opportunities[0].Bucket)
	}
	for _, bucket := range result.Top3.Buckets {
		if bucket != result.Opportunities[0].Bucket && bucket != result.Opportunities[1].Bucket && bucket != result.Opportunities[2].Bucket {
			t.Fatalf("top 3 scenario includes %q, want the three top opportunities", bucket)
		}
	}
	for _, bucket := range result.Recommended.Buckets {
		found := false
		for _, opportunity := range result.Opportunities {
			if opportunity.Bucket == bucket && opportunity.Delta.Overall >= recommendedPotentialDelta {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("recommended scenario includes %q without a >= %d overall gain", bucket, recommendedPotentialDelta)
		}
	}
}

func assertScenarioMatchesDirectRun(t *testing.T, pages []shared.CrawlPageSignal, issues []shared.CrawlIssueSignal, config shared.ScoringConfig, psi *shared.GooglePSIScoreInput, scenario PotentialScenario, name string) {
	t.Helper()
	simPSI := psi
	for _, bucket := range scenario.Buckets {
		if bucket == psiCoreWebVitalsBucket {
			perfect := 100
			simPSI = &shared.GooglePSIScoreInput{MobilePerformanceScore: &perfect}
			break
		}
	}
	direct := BuildScoreBreakdownWithConfig("crawl", pages, withoutBuckets(issues, scenario.Buckets), config, simPSI).CrawlScores()
	if scenario.Scores.Overall != direct.OverallScore ||
		scenario.Scores.SEO != direct.SEOScore ||
		scenario.Scores.AEO != direct.AEOScore ||
		scenario.Scores.PageSpeed != direct.PageSpeedScore {
		t.Fatalf("%s scenario = %+v, want direct rerun %+v", name, scenario.Scores, direct)
	}
}

func TestParseStoredGooglePSI(t *testing.T) {
	good := []byte(`[{"url":"https://example.com","mobile":{"success":true,"performance_score":82}}]`)
	if input := ParseStoredGooglePSI(good); input == nil || input.MobilePerformanceScore == nil || *input.MobilePerformanceScore != 82 {
		t.Fatalf("ParseStoredGooglePSI(good) = %+v, want score 82", input)
	}
	if input := ParseStoredGooglePSI(nil); input != nil {
		t.Fatalf("ParseStoredGooglePSI(nil) = %+v, want nil", input)
	}
	if input := ParseStoredGooglePSI([]byte(`[]`)); input != nil {
		t.Fatalf("ParseStoredGooglePSI(empty array) = %+v, want nil", input)
	}
	failed := []byte(`[{"url":"x","mobile":{"success":false,"performance_score":82}}]`)
	if input := ParseStoredGooglePSI(failed); input != nil {
		t.Fatalf("ParseStoredGooglePSI(failed mobile) = %+v, want nil", input)
	}
	if input := ParseStoredGooglePSI([]byte(`not json`)); input != nil {
		t.Fatalf("ParseStoredGooglePSI(garbage) = %+v, want nil", input)
	}
}
