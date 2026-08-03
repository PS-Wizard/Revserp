package issues

import (
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func issueTypesFor(derivedIssues []shared.DerivedIssue, url string) []string {
	types := make([]string, 0, len(derivedIssues))
	for _, issue := range derivedIssues {
		if issue.URL == url {
			types = append(types, issue.IssueType)
		}
	}
	return types
}

func healthyPageFact(url string) shared.PageFact {
	return shared.PageFact{
		URL:             url,
		ContentType:     "text/html",
		StatusCode:      200,
		Title:           "A reasonably descriptive page title goes here",
		MetaDescription: strings.Repeat("useful summary text ", 8),
		H1:              "A clear heading",
		H1Count:         1,
		H2Count:         2,
		WordCount:       400,
		VisibleText:     strings.Repeat("real content ", 200),
		CanonicalURL:    url,
		Viewport:        "width=device-width, initial-scale=1",
		Lang:            "en",
	}
}

// A broken page must report exactly one problem: that it is broken. Deriving the
// content rules over it would turn one problem into a pile of missing-metadata
// issues, which is the noise this split exists to prevent.
func TestDeriveIssuesGivesBrokenPagesOnlyTheirStatusIssue(t *testing.T) {
	tests := []struct {
		name      string
		pageFact  shared.PageFact
		wantIssue string
	}{
		{
			name:      "client error",
			pageFact:  shared.PageFact{URL: "https://example.com/gone", ContentType: "text/html", StatusCode: 404},
			wantIssue: "client_error_status",
		},
		{
			name:      "server error",
			pageFact:  shared.PageFact{URL: "https://example.com/boom", ContentType: "text/html", StatusCode: 503},
			wantIssue: "server_error_status",
		},
		{
			name:      "soft 404",
			pageFact:  shared.PageFact{URL: "https://example.com/soft", ContentType: "text/html", StatusCode: 200, Soft404: true},
			wantIssue: "soft_404",
		},
		{
			name:      "fetch failure",
			pageFact:  shared.PageFact{URL: "https://example.com/timeout", ContentType: "text/html", FetchError: "context deadline exceeded"},
			wantIssue: "fetch_failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			derivedIssues := DeriveIssues([]shared.PageFact{test.pageFact}, nil)
			gotTypes := issueTypesFor(derivedIssues, test.pageFact.URL)

			if len(gotTypes) != 1 || gotTypes[0] != test.wantIssue {
				t.Errorf("issues = %v, want exactly [%s]", gotTypes, test.wantIssue)
			}
		})
	}
}

// A soft 404 is picked over the status rule: it answered 200, so the status rule
// would say nothing at all.
func TestSoftNotFoundOutranksSuccessStatus(t *testing.T) {
	pageFact := shared.PageFact{URL: "https://example.com/soft", ContentType: "text/html", StatusCode: 200, Soft404: true}
	derivedIssues := DeriveIssues([]shared.PageFact{pageFact}, nil)

	if types := issueTypesFor(derivedIssues, pageFact.URL); len(types) != 1 || types[0] != "soft_404" {
		t.Errorf("issues = %v, want [soft_404]", types)
	}
}

// A fetch failure must not masquerade as missing content. This is the bug where a
// timed-out page persisted as a blank row and derived missing_title,
// missing_meta_description, and missing_h1.
func TestFetchFailureDoesNotDeriveMissingContentIssues(t *testing.T) {
	pageFact := shared.PageFact{URL: "https://example.com/timeout", ContentType: "text/html", FetchError: "dial tcp: i/o timeout"}
	derivedIssues := DeriveIssues([]shared.PageFact{pageFact}, nil)

	for _, issueType := range issueTypesFor(derivedIssues, pageFact.URL) {
		switch issueType {
		case "missing_title", "missing_meta_description", "missing_h1", "thin_content", "near_empty_visible_content":
			t.Errorf("fetch failure derived content issue %q", issueType)
		}
	}
}

func TestHealthyPagesStillDeriveContentIssues(t *testing.T) {
	// A page with no title/H1 is a real content problem and must still be caught.
	pageFact := shared.PageFact{URL: "https://example.com/thin", ContentType: "text/html", StatusCode: 200}
	derivedIssues := DeriveIssues([]shared.PageFact{pageFact}, nil)

	gotTypes := issueTypesFor(derivedIssues, pageFact.URL)
	for _, want := range []string{"missing_title", "missing_h1"} {
		found := false
		for _, got := range gotTypes {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("healthy page did not derive %q; got %v", want, gotTypes)
		}
	}
}

// A broken page next to a healthy one must not suppress the healthy page's rules.
func TestBrokenPageDoesNotSuppressHealthyPageDerivation(t *testing.T) {
	healthy := healthyPageFact("https://example.com/good")
	broken := shared.PageFact{URL: "https://example.com/gone", ContentType: "text/html", StatusCode: 404}

	derivedIssues := DeriveIssues([]shared.PageFact{broken, healthy}, nil)

	if types := issueTypesFor(derivedIssues, broken.URL); len(types) != 1 {
		t.Errorf("broken page issues = %v, want exactly one", types)
	}
	if len(issueTypesFor(derivedIssues, healthy.URL)) == 0 {
		t.Log("healthy page derived no issues, which is acceptable for a well-formed page")
	}
}

func TestIsScoreablePageExcludesUnhealthyPages(t *testing.T) {
	tests := []struct {
		name   string
		signal shared.CrawlPageSignal
		want   bool
	}{
		{"healthy html", shared.CrawlPageSignal{StatusCode: 200, ContentType: "text/html"}, true},
		{"client error", shared.CrawlPageSignal{StatusCode: 404, ContentType: "text/html"}, false},
		{"server error", shared.CrawlPageSignal{StatusCode: 500, ContentType: "text/html"}, false},
		{"soft 404", shared.CrawlPageSignal{StatusCode: 200, ContentType: "text/html", Soft404: true}, false},
		{"fetch error", shared.CrawlPageSignal{ContentType: "text/html", FetchError: "timeout"}, false},
		{"redirect", shared.CrawlPageSignal{StatusCode: 301, ContentType: "text/html"}, true},
		{"non-html", shared.CrawlPageSignal{StatusCode: 200, ContentType: "application/pdf"}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shared.IsScoreablePage(test.signal); got != test.want {
				t.Errorf("IsScoreablePage() = %v, want %v", got, test.want)
			}
		})
	}
}

// Broken pages stay out of the coverage denominator, so adding them to a crawl
// must not change any other issue's coverage. Without this, persisting broken
// pages would silently shift every score.
func TestBrokenPagesDoNotChangeCoverageDenominator(t *testing.T) {
	healthySignals := []shared.CrawlPageSignal{
		{URL: "https://example.com/a", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://example.com/b", StatusCode: 200, ContentType: "text/html"},
	}
	withBroken := append([]shared.CrawlPageSignal{}, healthySignals...)
	withBroken = append(withBroken,
		shared.CrawlPageSignal{URL: "https://example.com/gone", StatusCode: 404, ContentType: "text/html"},
		shared.CrawlPageSignal{URL: "https://example.com/soft", StatusCode: 200, ContentType: "text/html", Soft404: true},
	)

	if before, after := shared.CountScoreablePages(healthySignals), shared.CountScoreablePages(withBroken); before != after {
		t.Errorf("scoreable pages changed from %d to %d after adding broken pages", before, after)
	}

	issueSignals := []shared.CrawlIssueSignal{
		{URL: "https://example.com/a", Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
	}
	baseline := CalculateScores(healthySignals, issueSignals)
	withBrokenScores := CalculateScores(withBroken, issueSignals)

	if baseline.SEOScore != withBrokenScores.SEOScore {
		t.Errorf("SEO score changed from %d to %d purely by adding broken pages", baseline.SEOScore, withBrokenScores.SEOScore)
	}
}
