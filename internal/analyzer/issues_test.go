package analyzer

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDeriveIssuesBuildsExpectedPageIssues(t *testing.T) {
	pageFacts := []PageFact{
		{
			ID:                      pgtype.UUID{Valid: true},
			URL:                     "https://example.com/one",
			Depth:                   0,
			Title:                   "",
			MetaDescription:         "",
			WordCount:               120,
			ImageCount:              2,
			ImagesWithoutAltCount:   1,
			ImagesWithoutDimensions: 2,
			ResponseTimeMs:          1500,
		},
		{
			ID:              pgtype.UUID{Valid: true},
			URL:             "https://example.com/short-title",
			Depth:           1,
			Title:           "Short title",
			MetaDescription: "Too short",
			CanonicalURL:    "https://example.com/short-title",
		},
		{
			ID:              pgtype.UUID{Valid: true},
			URL:             "https://example.com/two",
			Depth:           2,
			Title:           "Useful Page",
			MetaDescription: "Summary",
			H1:              "Heading",
			H1Count:         2,
			WordCount:       400,
			CanonicalURL:    "https://example.com/elsewhere",
			Robots:          "noindex,nofollow",
			ResponseTimeMs:  200,
			OGTags:          []byte(`{"og:title":"Useful Page"}`),
		},
		{
			ID:              pgtype.UUID{Valid: true},
			URL:             "https://example.com/three",
			Depth:           1,
			Title:           "This is a very long page title that definitely exceeds sixty characters total",
			MetaDescription: "This meta description is intentionally made much longer than one hundred and sixty characters so that the backend issue derivation logic can flag it as too long for search engine snippet guidance.",
			H1:              "Heading",
			H1Count:         1,
			H2Count:         0,
			WordCount:       350,
			CanonicalURL:    "https://example.com/three",
			Viewport:        "width=device-width, initial-scale=1",
			Lang:            "en",
			SizeBytes:       2 * 1024 * 1024,
			JSONLD:          []byte(`[{"@type":"WebPage"}]`),
		},
		{
			ID:              pgtype.UUID{Valid: true},
			URL:             "https://example.com/four",
			Depth:           1,
			Title:           "This title is comfortably sized for search display",
			MetaDescription: "This meta description is intentionally written to land within the recommended range for search snippets and avoid any length warnings.",
			H1:              "Heading",
			H1Count:         1,
			H2Count:         2,
			WordCount:       420,
			CanonicalURL:    "https://example.com/four",
			Viewport:        "width=device-width, initial-scale=1",
			Lang:            "en",
			SizeBytes:       4 * 1024 * 1024,
			JSONLD:          []byte(`[{"@type":"Article"}]`),
			OGTags:          []byte(`{"og:title":"Example"}`),
		},
		{
			ID:              pgtype.UUID{Valid: true},
			URL:             "https://example.com/missing",
			Depth:           2,
			Title:           "Missing page title example for client error test",
			MetaDescription: "This description is comfortably within the recommended range for testing client error issue derivation.",
			H1:              "Missing Page",
			H1Count:         1,
			H2Count:         1,
			WordCount:       350,
			CanonicalURL:    "https://example.com/missing",
			Viewport:        "width=device-width, initial-scale=1",
			Lang:            "en",
			StatusCode:      404,
			JSONLD:          []byte(`[{"@type":"WebPage"}]`),
			OGTags:          []byte(`{"og:title":"Example"}`),
		},
		{
			ID:              pgtype.UUID{Valid: true},
			URL:             "https://example.com/error",
			Depth:           1,
			Title:           "Server error page title example for backend issue testing",
			MetaDescription: "This description is comfortably within the recommended range for testing server error issue derivation.",
			H1:              "Server Error",
			H1Count:         1,
			H2Count:         1,
			WordCount:       350,
			CanonicalURL:    "https://example.com/error",
			Viewport:        "width=device-width, initial-scale=1",
			Lang:            "en",
			StatusCode:      503,
			JSONLD:          []byte(`[{"@type":"WebPage"}]`),
			OGTags:          []byte(`{"og:title":"Example"}`),
		},
	}

	linkFacts := []LinkFact{
		{SourceURL: "https://example.com/one", TargetURL: "https://example.com/short-title"},
		{SourceURL: "https://example.com/one", TargetURL: "https://example.com/two"},
		{SourceURL: "https://example.com/short-title", TargetURL: "https://example.com/two"},
		{SourceURL: "https://example.com/four", TargetURL: "https://example.com/one"},
	}

	derivedIssues := DeriveIssues(pageFacts, linkFacts)
	assertIssueCode(t, derivedIssues, "missing_title")
	assertIssueCode(t, derivedIssues, "title_too_short")
	assertIssueCode(t, derivedIssues, "title_too_long")
	assertIssueCode(t, derivedIssues, "missing_meta_description")
	assertIssueCode(t, derivedIssues, "meta_description_too_short")
	assertIssueCode(t, derivedIssues, "meta_description_too_long")
	assertIssueCode(t, derivedIssues, "missing_h1")
	assertIssueCode(t, derivedIssues, "multiple_h1")
	assertIssueCode(t, derivedIssues, "missing_h2_on_long_page")
	assertIssueCode(t, derivedIssues, "thin_content")
	assertIssueCode(t, derivedIssues, "missing_canonical")
	assertIssueCode(t, derivedIssues, "canonical_differs")
	assertIssueCode(t, derivedIssues, "missing_viewport")
	assertIssueCode(t, derivedIssues, "missing_lang")
	assertIssueCode(t, derivedIssues, "noindex_page")
	assertIssueCode(t, derivedIssues, "nofollow_page")
	assertIssueCode(t, derivedIssues, "missing_og_tags")
	assertIssueCode(t, derivedIssues, "missing_structured_data")
	assertIssueCode(t, derivedIssues, "images_missing_alt")
	assertIssueCode(t, derivedIssues, "images_missing_dimensions")
	assertIssueCode(t, derivedIssues, "slow_response_time")
	assertIssueCode(t, derivedIssues, "moderate_page_size")
	assertIssueCode(t, derivedIssues, "large_page_size")
	assertIssueCode(t, derivedIssues, "client_error_status")
	assertIssueCode(t, derivedIssues, "server_error_status")
	assertIssueCode(t, derivedIssues, "no_internal_links_out")
	assertIssueCode(t, derivedIssues, "low_internal_links_out")
	assertIssueCode(t, derivedIssues, "low_internal_links_in")
}

func TestCountInternalLinksByPage(t *testing.T) {
	inboundInternalLinkCounts, outboundInternalLinkCounts := countInternalLinksByPage([]LinkFact{
		{SourceURL: "https://example.com/a", TargetURL: "https://example.com/b"},
		{SourceURL: "https://example.com/a", TargetURL: "https://example.com/b"},
		{SourceURL: "https://example.com/a", TargetURL: "https://example.com/c"},
		{SourceURL: "https://example.com/b", TargetURL: "https://example.com/c"},
	})

	if outboundInternalLinkCounts["https://example.com/a"] != 2 {
		t.Fatalf("got outbound count %d", outboundInternalLinkCounts["https://example.com/a"])
	}
	if inboundInternalLinkCounts["https://example.com/c"] != 2 {
		t.Fatalf("got inbound count %d", inboundInternalLinkCounts["https://example.com/c"])
	}
}

func TestHasMeaningfulOGTags(t *testing.T) {
	if hasMeaningfulOGTags(nil) {
		t.Fatalf("expected nil og tags to be empty")
	}
	if hasMeaningfulOGTags([]byte("null")) {
		t.Fatalf("expected null og tags to be empty")
	}
	if hasMeaningfulOGTags([]byte("{}")) {
		t.Fatalf("expected empty og tags object to be empty")
	}
	if !hasMeaningfulOGTags([]byte(`{"og:title":"Example"}`)) {
		t.Fatalf("expected og tags object to be meaningful")
	}
}

func TestHasMeaningfulJSONLD(t *testing.T) {
	if hasMeaningfulJSONLD(nil) {
		t.Fatalf("expected nil json-ld to be empty")
	}
	if hasMeaningfulJSONLD([]byte("null")) {
		t.Fatalf("expected null json-ld to be empty")
	}
	if hasMeaningfulJSONLD([]byte("[]")) {
		t.Fatalf("expected empty json-ld array to be empty")
	}
	if !hasMeaningfulJSONLD([]byte(`[{"@type":"WebPage"}]`)) {
		t.Fatalf("expected json-ld array to be meaningful")
	}
}

func assertIssueCode(t *testing.T, derivedIssues []DerivedIssue, issueCode string) {
	t.Helper()

	for _, derivedIssue := range derivedIssues {
		if derivedIssue.Code == issueCode {
			return
		}
	}

	t.Fatalf("missing issue code %q", issueCode)
}
