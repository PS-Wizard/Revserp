package seo

import (
	"context"
	"testing"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// extractionStatusPage builds a page whose body-derived counts would all fire:
// empty visible text, a low word count twin, and a long low-density twin.
func extractionStatusPage(url string, wordCount int32, imageCount int32) shared.PageFact {
	return shared.PageFact{
		URL:         url,
		WordCount:   wordCount,
		VisibleText: "",
		H2Count:     0,
		ImageCount:  imageCount,
	}
}

func issueTypesByURL(issues []shared.DerivedIssue) map[string]map[string]bool {
	byURL := make(map[string]map[string]bool)
	for _, issue := range issues {
		if byURL[issue.URL] == nil {
			byURL[issue.URL] = make(map[string]bool)
		}
		byURL[issue.URL][issue.IssueType] = true
	}
	return byURL
}

func TestExtractionFailedSuppressesBodyDependentIssues(t *testing.T) {
	t.Parallel()
	pages := []shared.PageFact{
		extractionStatusPage("https://example.com/failed-thin", 40, 0),
		extractionStatusPage("https://example.com/failed-long", 500, 12),
	}
	for index := range pages {
		pages[index].ContentExtractionStatus = "failed"
	}
	issues, _, err := DeriveIssuesWithDuplicateEvidenceWithContext(context.Background(), pages, nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	byURL := issueTypesByURL(issues)
	for _, url := range []string{"https://example.com/failed-thin", "https://example.com/failed-long"} {
		for _, suppressed := range []string{"thin_content", "near_empty_visible_content", "missing_h2_on_long_page", "too_many_images_on_page"} {
			if byURL[url][suppressed] {
				t.Fatalf("%s reports %s despite failed extraction", url, suppressed)
			}
		}
	}
	// Body-independent facts still report on failed pages.
	if !byURL["https://example.com/failed-thin"]["missing_title"] {
		t.Fatal("failed page lost body-independent missing_title")
	}
}

func TestExtractionStatusLegacyAndEmptyKeepBodyIssues(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"", "empty"} {
		pages := []shared.PageFact{
			extractionStatusPage("https://example.com/thin", 40, 0),
			extractionStatusPage("https://example.com/long", 500, 12),
		}
		for index := range pages {
			pages[index].ContentExtractionStatus = status
		}
		issues, _, err := DeriveIssuesWithDuplicateEvidenceWithContext(context.Background(), pages, nil)
		if err != nil {
			t.Fatalf("status %q derive: %v", status, err)
		}
		byURL := issueTypesByURL(issues)
		if !byURL["https://example.com/thin"]["thin_content"] {
			t.Fatalf("status %q lost thin_content", status)
		}
		if !byURL["https://example.com/thin"]["near_empty_visible_content"] {
			t.Fatalf("status %q lost near_empty_visible_content", status)
		}
		if !byURL["https://example.com/long"]["missing_h2_on_long_page"] {
			t.Fatalf("status %q lost missing_h2_on_long_page", status)
		}
		if !byURL["https://example.com/long"]["too_many_images_on_page"] {
			t.Fatalf("status %q lost too_many_images_on_page", status)
		}
		if !byURL["https://example.com/long"]["near_empty_visible_content"] {
			t.Fatalf("status %q lost near_empty_visible_content", status)
		}
	}
}

func TestExtractionFailedSkipsExactBodyDuplicate(t *testing.T) {
	t.Parallel()
	failed := []shared.PageFact{
		{URL: "https://example.com/failed-a", Title: "Same title", MetaDescription: "Same description text.", H1: "Same heading", ContentExtractionStatus: "failed"},
		{URL: "https://example.com/failed-b", Title: "Same title", MetaDescription: "Same description text.", H1: "Same heading", ContentExtractionStatus: "failed"},
	}
	if err := EnrichPageFactsWithContentFingerprintsWithContext(context.Background(), failed); err != nil {
		t.Fatalf("fingerprints: %v", err)
	}
	if failed[0].ContentSHA256 != "" || failed[1].ContentSHA256 != "" {
		t.Fatalf("failed pages must not hash, got %q %q", failed[0].ContentSHA256, failed[1].ContentSHA256)
	}
	issues, evidence, err := deriveDuplicateContentIssuesWithEvidence(context.Background(), failed)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	for _, issue := range issues {
		if issue.IssueType == "exact_duplicate_content" {
			t.Fatalf("failed pages grouped as fake exact duplicates: %+v", issue)
		}
	}
	for _, group := range evidence.Groups {
		if group.IssueType == DuplicateIssueTypeExactDuplicateContent {
			t.Fatalf("failed pages grouped as fake exact duplicates: %+v", group)
		}
	}
}

func TestExtractionFailedKeepsDuplicateTitleAndMeta(t *testing.T) {
	t.Parallel()
	pages := []shared.PageFact{
		{URL: "https://example.com/failed-a", Title: "Shared title", MetaDescription: "Shared description text here.", ContentExtractionStatus: "failed"},
		{URL: "https://example.com/failed-b", Title: "Shared title", MetaDescription: "Shared description text here.", ContentExtractionStatus: "failed"},
	}
	issues, _, err := DeriveIssuesWithDuplicateEvidenceWithContext(context.Background(), pages, nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	byURL := issueTypesByURL(issues)
	for _, url := range []string{"https://example.com/failed-a", "https://example.com/failed-b"} {
		if !byURL[url]["duplicate_title"] {
			t.Fatalf("%s lost body-independent duplicate_title", url)
		}
		if !byURL[url]["duplicate_meta_description"] {
			t.Fatalf("%s lost body-independent duplicate_meta_description", url)
		}
	}
}

func TestExtractionLegacyKeepsExactBodyDuplicate(t *testing.T) {
	t.Parallel()
	pages := []shared.PageFact{
		{URL: "https://example.com/a", Title: "Same title", MetaDescription: "Same description text.", H1: "Same heading", VisibleText: "Same body text here."},
		{URL: "https://example.com/b", Title: "Same title", MetaDescription: "Same description text.", H1: "Same heading", VisibleText: "Same body text here."},
	}
	if err := EnrichPageFactsWithContentFingerprintsWithContext(context.Background(), pages); err != nil {
		t.Fatalf("fingerprints: %v", err)
	}
	if pages[0].ContentSHA256 == "" || pages[0].ContentSHA256 != pages[1].ContentSHA256 {
		t.Fatalf("legacy identical content must hash equally, got %q %q", pages[0].ContentSHA256, pages[1].ContentSHA256)
	}
	// Blank the text so the near-duplicate engine path (covered elsewhere)
	// stays out of this exact-behavior test.
	pages[0].VisibleText, pages[1].VisibleText = "", ""
	issues, _, err := deriveDuplicateContentIssuesWithEvidence(context.Background(), pages)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	found := false
	for _, issue := range issues {
		if issue.IssueType == "exact_duplicate_content" {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy pages lost exact_duplicate_content")
	}
}
