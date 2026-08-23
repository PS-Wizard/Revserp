package issues

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func TestDeriveIssuesBuildsExpectedPageIssues(t *testing.T) {
	pageFacts := []shared.PageFact{{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/one", Depth: 0, Title: "", MetaDescription: "", WordCount: 120, ImageCount: 2, ImagesWithoutAltCount: 1, ImagesWithoutDimensions: 2, ResponseTimeMs: 1500}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/short-title", Depth: 1, Title: "Short title", MetaDescription: "Too short", CanonicalURL: "https://example.com/short-title"}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/two", Depth: 2, Title: "Useful Page", MetaDescription: "Summary", H1: "Heading", H1Count: 2, WordCount: 400, CanonicalURL: "https://example.com/elsewhere", Robots: "noindex,nofollow", ResponseTimeMs: 200, OGTags: []byte(`{"og:title":"Useful Page"}`)}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/three", Depth: 1, Title: "This is a very long page title that definitely exceeds sixty characters total", MetaDescription: "This meta description is intentionally made much longer than one hundred and sixty characters so that the backend issue derivation logic can flag it as too long for search engine snippet guidance.", H1: "Heading", H1Count: 1, H2Count: 0, WordCount: 350, CanonicalURL: "https://example.com/three", Viewport: "width=device-width, initial-scale=1", Lang: "en", SizeBytes: 2 * 1024 * 1024, JSONLD: []byte(`[{"@type":"WebPage"}]`)}, {ID: pgtype.UUID{Valid: true}, URL: "http://example.com/four", Depth: 1, Title: "This title is comfortably sized for search display", MetaDescription: "This meta description is intentionally written to land within the recommended range for search snippets and avoid any length warnings.", H1: "Heading", H1Count: 1, H2Count: 2, WordCount: 420, CanonicalURL: "http://example.com/four", Viewport: "width=device-width, initial-scale=1", Lang: "en", SizeBytes: 4 * 1024 * 1024, JSONLD: []byte(`[{"@type":"Article"}]`), OGTags: []byte(`{"og:title":"Example"}`)}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/missing", Depth: 2, Title: "Missing page title example for client error test", MetaDescription: "This description is comfortably within the recommended range for testing client error issue derivation.", H1: "Missing Page", H1Count: 1, H2Count: 1, WordCount: 350, CanonicalURL: "https://example.com/missing", Viewport: "width=device-width, initial-scale=1", Lang: "en", StatusCode: 404, JSONLD: []byte(`[{"@type":"WebPage"}]`), OGTags: []byte(`{"og:title":"Example"}`)}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/error", Depth: 1, Title: "Server error page title example for backend issue testing", MetaDescription: "This description is comfortably within the recommended range for testing server error issue derivation.", H1: "Server Error", H1Count: 1, H2Count: 1, WordCount: 350, CanonicalURL: "https://example.com/error", Viewport: "width=device-width, initial-scale=1", Lang: "en", StatusCode: 503, JSONLD: []byte(`[{"@type":"WebPage"}]`), OGTags: []byte(`{"og:title":"Example"}`)}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/image-heavy", Depth: 1, Title: "Image heavy content page example for media optimization checks", MetaDescription: "This description is comfortably within the recommended range for testing image density issue derivation.", H1: "Image Heavy Content", H1Count: 1, H2Count: 1, WordCount: 400, ImageCount: 10, CanonicalURL: "https://example.com/image-heavy", Viewport: "width=device-width, initial-scale=1", Lang: "en", JSONLD: []byte(`[{"@type":"WebPage"}]`), OGTags: []byte(`{"og:title":"Example"}`)}}
	linkFacts := []shared.LinkFact{{SourceURL: "https://example.com/one", TargetURL: "https://example.com/short-title"}, {SourceURL: "https://example.com/one", TargetURL: "https://example.com/two"}, {SourceURL: "https://example.com/short-title", TargetURL: "https://example.com/two"}, {SourceURL: "https://example.com/four", TargetURL: "https://example.com/one"}}

	derivedIssues := DeriveIssues(pageFacts, linkFacts, shared.SiteFacts{})
	assertIssueType(t, derivedIssues, "missing_title")
	assertIssueType(t, derivedIssues, "title_too_short")
	assertIssueType(t, derivedIssues, "title_too_long")
	assertIssueType(t, derivedIssues, "missing_meta_description")
	assertIssueType(t, derivedIssues, "meta_description_too_short")
	assertIssueType(t, derivedIssues, "meta_description_too_long")
	assertIssueType(t, derivedIssues, "missing_h1")
	assertIssueType(t, derivedIssues, "multiple_h1")
	assertIssueType(t, derivedIssues, "missing_h2_on_long_page")
	assertIssueType(t, derivedIssues, "thin_content")
	assertIssueType(t, derivedIssues, "missing_canonical")
	assertIssueType(t, derivedIssues, "canonical_differs")
	assertIssueType(t, derivedIssues, "missing_viewport")
	assertIssueType(t, derivedIssues, "missing_lang")
	assertIssueType(t, derivedIssues, "noindex_page")
	assertIssueType(t, derivedIssues, "nofollow_page")
	assertIssueType(t, derivedIssues, "missing_og_tags")
	assertIssueBucket(t, derivedIssues, "missing_og_tags", "trust")
	assertIssueType(t, derivedIssues, "missing_structured_data")
	assertIssueType(t, derivedIssues, "missing_https")
	assertIssueType(t, derivedIssues, "missing_author_signal")
	assertIssueType(t, derivedIssues, "missing_external_citations")
	assertIssueType(t, derivedIssues, "images_missing_alt")
	assertIssueType(t, derivedIssues, "images_missing_dimensions")
	assertIssueType(t, derivedIssues, "too_many_images_on_page")
	assertIssueDetailContains(t, derivedIssues, "too_many_images_on_page", "Page has 10 images and 400 words")
	assertIssueType(t, derivedIssues, "slow_response_time")
	assertIssueDetailContains(t, derivedIssues, "slow_response_time", "1500ms")
	assertIssueType(t, derivedIssues, "moderate_page_size")
	assertIssueType(t, derivedIssues, "large_page_size")
	assertIssueType(t, derivedIssues, "client_error_status")
	assertIssueType(t, derivedIssues, "server_error_status")
	assertIssueType(t, derivedIssues, "no_internal_links_out")
	assertIssueType(t, derivedIssues, "low_internal_links_out")
	assertIssueType(t, derivedIssues, "low_internal_links_in")
}

func TestDeriveIssuesBuildsDuplicateContentIssues(t *testing.T) {
	pageFacts := []shared.PageFact{{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/original", Title: "Technical SEO audit service", MetaDescription: "Enterprise technical SEO audits and monitoring.", H1: "Technical SEO audit service", VisibleText: "We provide enterprise technical SEO audits, issue monitoring, crawl analysis, and monthly reporting for growing software companies."}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/original-copy", Title: "Technical SEO audit service", MetaDescription: "Enterprise technical SEO audits and monitoring.", H1: "Technical SEO audit service", VisibleText: "We provide enterprise technical SEO audits, issue monitoring, crawl analysis, and monthly reporting for growing software companies."}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/original-variant", Title: "Enterprise technical SEO auditing services", MetaDescription: "Technical SEO audits, crawl monitoring, and reporting for software teams.", H1: "Enterprise technical SEO auditing services", VisibleText: "We help software companies with crawl analysis, technical SEO monitoring, enterprise audits, and monthly reporting to surface search performance issues."}, {ID: pgtype.UUID{Valid: true}, URL: "https://example.com/different", Title: "Brand strategy workshop", MetaDescription: "Messaging workshops for product teams.", H1: "Brand strategy workshop", VisibleText: "We run positioning workshops, customer interviews, homepage messaging sessions, and brand narrative reviews for internal product marketing teams."}}
	derivedIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{})
	assertIssueType(t, derivedIssues, "exact_duplicate_content")
	assertIssueType(t, derivedIssues, "near_duplicate_content")
}

func TestDeriveIssuesBuildsSkippedHeadingLevelsIssue(t *testing.T) {
	pageFacts := []shared.PageFact{{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/heading-outline", Title: "Heading outline example page title for structure testing", MetaDescription: "This meta description is comfortably within the recommended range for structure issue testing.", H1: "Heading outline example", H1Count: 1, H2Count: 2, WordCount: 350, CanonicalURL: "https://example.com/heading-outline", Viewport: "width=device-width, initial-scale=1", Lang: "en", OGTags: []byte(`{"og:title":"Example"}`), JSONLD: []byte(`[{"@type":"WebPage"}]`), HeadingOutline: []byte(`[{"level":1,"text":"Overview"},{"level":3,"text":"Deep dive"},{"level":2,"text":"Details"},{"level":4,"text":"Implementation"}]`)}}
	derivedIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{})
	assertIssueType(t, derivedIssues, "skipped_heading_levels")
	assertIssueDetailContains(t, derivedIssues, "skipped_heading_levels", `H1 "Overview" jumps to H3 "Deep dive".`)
	assertIssueDetailContains(t, derivedIssues, "skipped_heading_levels", `H2 "Details" jumps to H4 "Implementation".`)
}

func TestCalculateScoresUsesPillarScopedIssueBuckets(t *testing.T) {
	crawlPageSignals := []shared.CrawlPageSignal{}
	for pageIndex := 0; pageIndex < 12; pageIndex++ {
		crawlPageSignals = append(crawlPageSignals, shared.CrawlPageSignal{URL: "https://example.com/page-" + string(rune('a'+pageIndex)), ContentType: "text/html; charset=utf-8"})
	}
	crawlIssueSignals := []shared.CrawlIssueSignal{}
	for pageIndex := 0; pageIndex < 6; pageIndex++ {
		pageURL := crawlPageSignals[pageIndex].URL
		crawlIssueSignals = append(crawlIssueSignals,
			shared.CrawlIssueSignal{URL: pageURL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
			shared.CrawlIssueSignal{URL: pageURL, Pillar: "aeo", Bucket: "answerability", Severity: "high", IssueType: "missing_structured_data"},
			shared.CrawlIssueSignal{URL: pageURL, Pillar: "pagespeed", Bucket: "server_responsiveness", Severity: "high", IssueType: "slow_response_time"},
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

func assertIssueType(t *testing.T, derivedIssues []shared.DerivedIssue, issueType string) {
	t.Helper()
	for _, derivedIssue := range derivedIssues {
		if derivedIssue.IssueType == issueType {
			return
		}
	}
	t.Fatalf("missing issue type %q", issueType)
}

func assertIssueDetailContains(t *testing.T, derivedIssues []shared.DerivedIssue, issueType string, expectedDetailFragment string) {
	t.Helper()
	for _, derivedIssue := range derivedIssues {
		if derivedIssue.IssueType != issueType {
			continue
		}
		if !strings.Contains(derivedIssue.Details, expectedDetailFragment) {
			t.Fatalf("issue type %q details %q did not contain %q", issueType, derivedIssue.Details, expectedDetailFragment)
		}
		return
	}
	t.Fatalf("missing issue type %q", issueType)
}

func assertIssueBucket(t *testing.T, derivedIssues []shared.DerivedIssue, issueType string, expectedBucket string) {
	t.Helper()
	for _, derivedIssue := range derivedIssues {
		if derivedIssue.IssueType != issueType {
			continue
		}
		if derivedIssue.Bucket != expectedBucket {
			t.Fatalf("issue type %q bucket %q did not match %q", issueType, derivedIssue.Bucket, expectedBucket)
		}
		return
	}
	t.Fatalf("missing issue type %q", issueType)
}

func TestDeriveIssuesSkipsNonHTMLPages(t *testing.T) {
	pageFacts := []shared.PageFact{
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/report.pdf", ContentType: "application/pdf"},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/page", ContentType: "text/html", Title: "", MetaDescription: "", WordCount: 120},
	}

	derivedIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{})
	for _, derivedIssue := range derivedIssues {
		if derivedIssue.URL == "https://example.com/report.pdf" {
			t.Fatalf("expected non-html page to be skipped, but got issue %q", derivedIssue.IssueType)
		}
	}
	assertIssueType(t, derivedIssues, "missing_title")
}

func TestCalculateScoresScalesCoverageByCrawlSize(t *testing.T) {
	smallCrawlPages := []shared.CrawlPageSignal{}
	for pageIndex := 0; pageIndex < 10; pageIndex++ {
		smallCrawlPages = append(smallCrawlPages, shared.CrawlPageSignal{URL: "https://example.com/small-page-" + string(rune('a'+pageIndex)), ContentType: "text/html; charset=utf-8"})
	}
	largeCrawlPages := []shared.CrawlPageSignal{}
	for pageIndex := 0; pageIndex < 100; pageIndex++ {
		largeCrawlPages = append(largeCrawlPages, shared.CrawlPageSignal{URL: "https://example.com/large-page-" + string(rune('a'+pageIndex%26)) + "-" + string(rune('a'+(pageIndex/26))), ContentType: "text/html; charset=utf-8"})
	}

	sharedIssuesOnThreePages := []shared.CrawlIssueSignal{
		{URL: smallCrawlPages[0].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
		{URL: smallCrawlPages[1].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
		{URL: smallCrawlPages[2].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
	}
	largeIssuesOnThreePages := []shared.CrawlIssueSignal{
		{URL: largeCrawlPages[0].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
		{URL: largeCrawlPages[1].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
		{URL: largeCrawlPages[2].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: "high", IssueType: "missing_title"},
	}

	smallScores := CalculateScores(smallCrawlPages, sharedIssuesOnThreePages)
	largeScores := CalculateScores(largeCrawlPages, largeIssuesOnThreePages)

	if smallScores.SEOScore >= largeScores.SEOScore {
		t.Fatalf("expected the same affected page count to hurt a smaller crawl more, got small=%d large=%d", smallScores.SEOScore, largeScores.SEOScore)
	}
}

func TestCalculateScoresSoftSumsBucketPenalties(t *testing.T) {
	crawlPages := []shared.CrawlPageSignal{}
	for pageIndex := 0; pageIndex < 20; pageIndex++ {
		crawlPages = append(crawlPages, shared.CrawlPageSignal{URL: "https://example.com/page-" + string(rune('a'+pageIndex)), ContentType: "text/html; charset=utf-8"})
	}

	issueTypes := []struct {
		issueType string
		severity  string
	}{
		{"missing_title", "high"},
		{"duplicate_title", "high"},
		{"missing_meta_description", "medium"},
		{"title_too_short", "medium"},
		{"meta_description_too_short", "medium"},
		{"title_too_long", "medium"},
	}
	issues := []shared.CrawlIssueSignal{}
	for _, definition := range issueTypes {
		for pageIndex := 0; pageIndex < 8; pageIndex++ {
			issues = append(issues, shared.CrawlIssueSignal{URL: crawlPages[pageIndex].URL, Pillar: "seo", Bucket: "serp_metadata", Severity: definition.severity, IssueType: definition.issueType})
		}
	}

	breakdown := BuildScoreBreakdown("", crawlPages, issues, nil)
	bucket := findBucketBreakdown(t, findPillarBreakdown(t, breakdown, "seo"), "serp_metadata")

	if len(bucket.Issues) < 2 {
		t.Fatalf("expected multiple issue types in serp_metadata, got %d", len(bucket.Issues))
	}
	naiveSum := 0.0
	for _, issue := range bucket.Issues {
		naiveSum += issue.FinalPenalty
	}
	largestPenalty := bucket.Issues[0].FinalPenalty

	if bucket.TotalPenalty >= naiveSum {
		t.Fatalf("expected soft-sum penalty %.2f to stay below the naive additive sum %.2f", bucket.TotalPenalty, naiveSum)
	}
	if bucket.TotalPenalty < largestPenalty {
		t.Fatalf("expected soft-sum penalty %.2f to be at least the worst single issue %.2f", bucket.TotalPenalty, largestPenalty)
	}
	if bucket.TotalPenalty > largestPenalty*2 {
		t.Fatalf("expected soft-sum penalty %.2f to be bounded near 2x the worst issue %.2f", bucket.TotalPenalty, largestPenalty)
	}
	if bucket.Score <= 0 {
		t.Fatalf("expected coexisting issue types not to free-fall the bucket to zero, got score %d", bucket.Score)
	}
}

func findPillarBreakdown(t *testing.T, breakdown shared.ScoreBreakdownSnapshot, pillarID string) shared.PillarScoreBreakdown {
	t.Helper()
	for _, pillarBreakdown := range breakdown.Pillars {
		if pillarBreakdown.ID == pillarID {
			return pillarBreakdown
		}
	}
	t.Fatalf("missing pillar breakdown %q", pillarID)
	return shared.PillarScoreBreakdown{}
}

func findBucketBreakdown(t *testing.T, pillarBreakdown shared.PillarScoreBreakdown, bucketID string) shared.BucketScoreBreakdown {
	t.Helper()
	for _, bucketBreakdown := range pillarBreakdown.Buckets {
		if bucketBreakdown.ID == bucketID {
			return bucketBreakdown
		}
	}
	t.Fatalf("missing bucket breakdown %q", bucketID)
	return shared.BucketScoreBreakdown{}
}

func TestDeriveIssuesBuildsAdditionalAEOIssues(t *testing.T) {
	pageFacts := []shared.PageFact{
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/", Title: "Home", MetaDescription: "Homepage description long enough for the fixture.", H1: "Home", H1Count: 1, VisibleText: "Homepage content with enough words to count as a real page in the fixture.", WordCount: 180, CanonicalURL: "https://example.com/", Viewport: "width=device-width, initial-scale=1", Lang: "en", OGTags: []byte(`{"og:title":"Home"}`), JSONLD: []byte(`[{"@type":"WebPage"}]`)},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/blog/post-one", Title: "Post one", MetaDescription: "Article description long enough for the fixture to avoid metadata warnings.", H1: "Post one", H1Count: 1, VisibleText: "This is a long article page with enough words to be treated as editorial content for the AEO checks in this test fixture.", WordCount: 700, CanonicalURL: "https://example.com/blog/post-one", Viewport: "width=device-width, initial-scale=1", Lang: "en", Author: "admin", ExternalLinks: 1, JSONLD: []byte(`[{"@type":"WebPage"}]`)},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/blog/post-two", Title: "Post two", MetaDescription: "Second article description long enough for the fixture to avoid metadata warnings.", H1: "Post two", H1Count: 1, VisibleText: "This is another article page with enough words to be treated as editorial content for the AEO checks in this test fixture.", WordCount: 900, CanonicalURL: "https://example.com/blog/post-two", Viewport: "width=device-width, initial-scale=1", Lang: "en", Author: "Jane Doe", JSONLD: []byte(`[{"@type":"Article","name":"Post two","url":"https://example.com/blog/post-two","description":"Description only for the page."}]`)},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/blog/post-three", Title: "Post three", MetaDescription: "Third article description long enough for the fixture to avoid metadata warnings.", H1: "Post three", H1Count: 1, VisibleText: "This is another article page with enough words to be treated as editorial content for the AEO checks in this test fixture.", WordCount: 1300, CanonicalURL: "https://example.com/blog/post-three", Viewport: "width=device-width, initial-scale=1", Lang: "en", Author: "Jane Doe", ExternalLinks: 1, JSONLD: []byte(`[{"@type":"Article","author":"Someone Else","publisher":"Editorial Desk","url":"https://example.com/blog/post-three"}]`)},

		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/faq", Title: "FAQ", MetaDescription: "FAQ description long enough for the fixture to avoid metadata warnings.", H1: "FAQ", H1Count: 1, VisibleText: "What is this? How does it work? Where can I start?", WordCount: 220, CanonicalURL: "https://example.com/faq", Viewport: "width=device-width, initial-scale=1", Lang: "en", HeadingOutline: []byte(`[{"level":2,"text":"What is this?"},{"level":2,"text":"How does it work?"}]`)},
	}

	derivedIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{})
	assertIssueType(t, derivedIssues, "weak_author_signal")
	assertIssueType(t, derivedIssues, "generic_structured_data_only")
	assertIssueType(t, derivedIssues, "schema_missing_core_fields")
	assertIssueType(t, derivedIssues, "article_missing_article_schema")
	assertIssueType(t, derivedIssues, "article_missing_publisher_identity")
	assertIssueType(t, derivedIssues, "author_signal_not_supported_by_schema")
	assertIssueType(t, derivedIssues, "long_article_has_no_citations")
	assertIssueType(t, derivedIssues, "long_article_has_weak_citations")
	assertIssueType(t, derivedIssues, "missing_org_identity_schema")
	assertIssueType(t, derivedIssues, "missing_website_schema")
	assertIssueType(t, derivedIssues, "missing_about_page")
	assertIssueType(t, derivedIssues, "missing_contact_page")
	assertIssueType(t, derivedIssues, "missing_policy_page")
	assertIssueType(t, derivedIssues, "weak_open_graph_coverage")
	assertIssueType(t, derivedIssues, "homepage_missing_org_contact_trust_signals")
	assertIssueType(t, derivedIssues, "faq_like_page_missing_faq_schema")
}

func TestCalculateScoresAppliesAEOIssueCoverageFloor(t *testing.T) {
	crawlPageSignals := make([]shared.CrawlPageSignal, 0, 20)
	for pageIndex := 0; pageIndex < 20; pageIndex++ {
		crawlPageSignals = append(crawlPageSignals, shared.CrawlPageSignal{
			URL:         "https://example.com/page-" + string(rune('a'+pageIndex)),
			ContentType: "text/html; charset=utf-8",
		})
	}

	crawlIssueSignals := []shared.CrawlIssueSignal{
		{URL: crawlPageSignals[0].URL, Pillar: "aeo", Bucket: "trust", Severity: "medium", IssueType: "missing_website_schema"},
		{URL: crawlPageSignals[0].URL, Pillar: "aeo", Bucket: "authoritativeness", Severity: "high", IssueType: "missing_org_identity_schema"},
		{URL: crawlPageSignals[1].URL, Pillar: "aeo", Bucket: "answerability", Severity: "medium", IssueType: "generic_structured_data_only"},
		{URL: crawlPageSignals[2].URL, Pillar: "aeo", Bucket: "expertise", Severity: "medium", IssueType: "missing_author_signal"},
	}

	scores := CalculateScores(crawlPageSignals, crawlIssueSignals)
	if scores.AEOScore > 96 {
		t.Fatalf("expected sparse AEO issues to reduce the AEO score more aggressively, got %d", scores.AEOScore)
	}
}

func TestDeriveIssuesBuildsAdditionalHighValueSEOIssues(t *testing.T) {
	pageFacts := []shared.PageFact{
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/a", Title: "Platform Pricing", MetaDescription: "Compare plans and pricing for the platform.", H1: "Enterprise migration services", H1Count: 1, VisibleText: "tiny words only", WordCount: 40, CanonicalURL: "not a valid canonical", Viewport: "width=device-width, initial-scale=1", Lang: "en"},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/b", Title: "Platform Pricing", MetaDescription: "Compare plans and pricing for the platform.", H1: "Platform Pricing", H1Count: 1, VisibleText: "This page has enough visible text to avoid the near empty content check while still participating in duplicate metadata detection.", WordCount: 120, CanonicalURL: "https://example.com/canonical-target", Viewport: "width=device-width, initial-scale=1", Lang: "en"},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/canonical-target", Title: "Canonical target", MetaDescription: "Canonical target description that is long enough for the test fixture.", H1: "Canonical target", H1Count: 1, VisibleText: "This canonical target page exists but should be treated as non-indexable because of the robots directive.", WordCount: 120, CanonicalURL: "https://example.com/canonical-target", Viewport: "width=device-width, initial-scale=1", Lang: "en", Robots: "noindex"},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/deep", Title: "Deep archive entry", MetaDescription: "Deep archive entry description that is long enough for this test fixture.", H1: "Deep archive entry", H1Count: 1, VisibleText: "This page has enough content but it sits very deep in the crawl and has no internal links pointing to it.", WordCount: 120, CanonicalURL: "https://example.com/deep", Viewport: "width=device-width, initial-scale=1", Lang: "en", Depth: 5},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/source", Title: "Source page", MetaDescription: "Source page description that is long enough for this test fixture.", H1: "Source page", H1Count: 1, VisibleText: "This page contains internal links to a broken target and a redirecting target.", WordCount: 120, CanonicalURL: "https://example.com/source", Viewport: "width=device-width, initial-scale=1", Lang: "en"},
	}
	linkFacts := []shared.LinkFact{
		{SourceURL: "https://example.com/source", TargetURL: "https://example.com/broken", TargetStatus: 404},
		{SourceURL: "https://example.com/source", TargetURL: "https://example.com/redirect", TargetStatus: 301},
		{SourceURL: "https://example.com/a", TargetURL: "https://example.com/source", TargetStatus: 200},
		{SourceURL: "https://example.com/b", TargetURL: "https://example.com/source", TargetStatus: 200},
	}

	derivedIssues := DeriveIssues(pageFacts, linkFacts, shared.SiteFacts{})
	assertIssueType(t, derivedIssues, "duplicate_title")
	assertIssueType(t, derivedIssues, "duplicate_meta_description")
	assertIssueType(t, derivedIssues, "near_empty_visible_content")
	assertIssueType(t, derivedIssues, "title_h1_mismatch")
	assertIssueType(t, derivedIssues, "malformed_canonical")
	assertIssueType(t, derivedIssues, "canonical_points_to_non_indexable_page")
	assertIssueType(t, derivedIssues, "orphan_like_page")
	assertIssueType(t, derivedIssues, "very_deep_page")
	assertIssueType(t, derivedIssues, "internal_links_to_broken_pages")
	assertIssueType(t, derivedIssues, "internal_links_to_redirects")
}

func TestDeriveIssuesSkipsPurePaginationDuplicateMetadataGroups(t *testing.T) {
	pageFacts := []shared.PageFact{
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/blog?page=2", Title: "Blog archive", MetaDescription: "Browse the blog archive.", H1: "Blog archive", H1Count: 1, VisibleText: "Archive page two content.", WordCount: 120, CanonicalURL: "https://example.com/blog?page=2", Viewport: "width=device-width, initial-scale=1", Lang: "en"},
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/blog?page=3", Title: "Blog archive", MetaDescription: "Browse the blog archive.", H1: "Blog archive", H1Count: 1, VisibleText: "Archive page three content.", WordCount: 120, CanonicalURL: "https://example.com/blog?page=3", Viewport: "width=device-width, initial-scale=1", Lang: "en"},
	}

	derivedIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{})
	assertIssueTypeMissing(t, derivedIssues, "duplicate_title")
	assertIssueTypeMissing(t, derivedIssues, "duplicate_meta_description")
}

func TestRecommendedFixReturnsCuratedGuidance(t *testing.T) {
	testCases := []struct {
		issueType string
		expected  string
	}{
		{issueType: "title_too_long", expected: "Shorten the title to roughly 30-60 characters, keep the main intent first, and remove filler or repeated brand text."},
		{issueType: "missing_structured_data", expected: "Add page-appropriate JSON-LD structured data using a schema type that matches the page purpose."},
		{issueType: "exact_duplicate_content", expected: "Consolidate duplicate pages into a canonical source, merge overlapping copy, and redirect or canonicalize true duplicates."},
	}

	for _, testCase := range testCases {
		recommendedFix := RecommendedFix("seo", "content_quality", testCase.issueType, "", "")
		if recommendedFix != testCase.expected {
			t.Fatalf("issue type %q recommended fix %q did not match %q", testCase.issueType, recommendedFix, testCase.expected)
		}
	}
}

func TestRecommendedFixFallsBackToIssueContext(t *testing.T) {
	recommendedFix := RecommendedFix(
		"seo",
		"content_quality",
		"custom_issue_type",
		"Custom issue message",
		"Custom issue details.",
	)

	if !strings.Contains(recommendedFix, "Custom Issue Type") {
		t.Fatalf("expected fallback recommended fix to mention the humanized issue type, got %q", recommendedFix)
	}
	if !strings.Contains(recommendedFix, "Content Quality") {
		t.Fatalf("expected fallback recommended fix to mention the humanized bucket, got %q", recommendedFix)
	}
	if !strings.Contains(recommendedFix, "Custom issue message") {
		t.Fatalf("expected fallback recommended fix to mention the issue message, got %q", recommendedFix)
	}
	if !strings.Contains(recommendedFix, "Custom issue details.") {
		t.Fatalf("expected fallback recommended fix to mention the issue details, got %q", recommendedFix)
	}
}

func assertIssueTypeMissing(t *testing.T, derivedIssues []shared.DerivedIssue, issueType string) {
	t.Helper()
	for _, derivedIssue := range derivedIssues {
		if derivedIssue.IssueType == issueType {
			t.Fatalf("unexpected issue type %q", issueType)
		}
	}
}

func TestDeriveIssuesHandlesMissingLlmsTxt(t *testing.T) {
	pageFacts := []shared.PageFact{
		{ID: pgtype.UUID{Valid: true}, URL: "https://example.com/", Depth: 0, Title: "Home", MetaDescription: "desc", H1: "Home", H1Count: 1, WordCount: 200, CanonicalURL: "https://example.com/", Viewport: "width=device-width", Lang: "en", OGTags: []byte(`{"og:title":"Home"}`), JSONLD: []byte(`[{"@type":"WebSite","name":"x","url":"https://example.com/"}]`)},
	}
	// NULL -> no issue (old crawls not penalized)
	nullIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{HasLlmsTxt: pgtype.Bool{Valid: false}})
	assertIssueTypeMissing(t, nullIssues, "missing_llms_txt")
	// true -> no issue
	trueIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{HasLlmsTxt: pgtype.Bool{Bool: true, Valid: true}})
	assertIssueTypeMissing(t, trueIssues, "missing_llms_txt")
	// false -> issue emitted, high severity, trust bucket, aeo pillar
	falseIssues := DeriveIssues(pageFacts, nil, shared.SiteFacts{HasLlmsTxt: pgtype.Bool{Bool: false, Valid: true}})
	assertIssueType(t, falseIssues, "missing_llms_txt")
	assertIssueBucket(t, falseIssues, "missing_llms_txt", "trust")
	for _, iss := range falseIssues {
		if iss.IssueType == "missing_llms_txt" {
			if iss.Pillar != "aeo" {
				t.Fatalf("expected pillar aeo, got %q", iss.Pillar)
			}
			if iss.Severity != "high" {
				t.Fatalf("expected severity high, got %q", iss.Severity)
			}
			if iss.Message != "Site is missing an /llms.txt file" {
				t.Fatalf("unexpected message %q", iss.Message)
			}
			if !strings.Contains(iss.Details, "llmstxt.org") {
				t.Fatalf("expected details to mention llmstxt.org, got %q", iss.Details)
			}
		}
	}
}

func TestCalculateScoresAppliesMissingLlmsTxtPenalty(t *testing.T) {
	crawlPages := []shared.CrawlPageSignal{
		{URL: "https://example.com/", ContentType: "text/html"},
		{URL: "https://example.com/about", ContentType: "text/html"},
	}
	scoresNoIssue := CalculateScores(crawlPages, nil)
	scoresWithLlms := CalculateScores(crawlPages, []shared.CrawlIssueSignal{
		{URL: "https://example.com/", Pillar: "aeo", Bucket: "trust", Severity: "high", IssueType: "missing_llms_txt"},
	})
	if scoresWithLlms.AEOScore >= scoresNoIssue.AEOScore {
		t.Fatalf("expected missing_llms_txt to reduce AEO score, got without %d with %d", scoresNoIssue.AEOScore, scoresWithLlms.AEOScore)
	}
	if RecommendedFix("aeo", "trust", "missing_llms_txt", "", "") == "" {
		t.Fatalf("expected curated fix for missing_llms_txt")
	}
}
