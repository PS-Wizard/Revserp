package worker

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestNormalizeGooglePSIPageURLUsesOrigin(t *testing.T) {
	got, err := normalizeGooglePSIPageURL("https://example.com/articles/post?utm=1")
	if err != nil {
		t.Fatalf("normalizeGooglePSIPageURL returned error: %v", err)
	}
	if got != "https://example.com/" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractGooglePSIPerformanceScore(t *testing.T) {
	score := 0.734
	response := googlePSIAPIResponse{}
	response.LighthouseResult.Categories = map[string]struct {
		Score *float64 `json:"score"`
	}{
		"performance": {Score: &score},
	}

	got := extractGooglePSIPerformanceScore(response)
	if got == nil || *got != 73 {
		t.Fatalf("got %v", got)
	}
}

func TestBuildGooglePSIIssues(t *testing.T) {
	score := 42
	lcp := 4.2
	cls := 0.3
	issues := buildGooglePSIIssues(testCrawlUUID(), googlePSIStoredResult{
		URL: "https://example.com/",
		Mobile: googlePSIDeviceResult{
			Success:          true,
			PerformanceScore: &score,
			Metrics: googlePSIMetrics{
				LargestContentfulPaint: &lcp,
				CumulativeLayoutShift:  &cls,
			},
		},
	})

	if len(issues) != 3 {
		t.Fatalf("got %d issues", len(issues))
	}
	if issues[0].Bucket != "psi_cwv" || issues[0].IssueType != "google_psi_mobile_performance" || issues[0].Severity != "high" {
		t.Fatalf("unexpected first issue: %+v", issues[0])
	}
}

func testCrawlUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
}
