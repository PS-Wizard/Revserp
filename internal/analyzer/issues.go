package analyzer

import (
	"bytes"
	"fmt"
	"strings"
)

const thinContentWordCountThreshold = 150
const longPageWordCountThreshold = 300
const shortTitleCharacterThreshold = 30
const longTitleCharacterThreshold = 60
const shortMetaDescriptionCharacterThreshold = 120
const longMetaDescriptionCharacterThreshold = 160
const slowResponseTimeMillisecondsThreshold = 1000
const moderatePageSizeBytesThreshold = 1 * 1024 * 1024
const largePageSizeBytesThreshold = 3 * 1024 * 1024
const lowInternalLinksOutThreshold = 2
const lowInternalLinksInThreshold = 1

// DeriveIssues builds backend issue rows from persisted crawl facts.
func DeriveIssues(pageFacts []PageFact, linkFacts []LinkFact) []DerivedIssue {
	var derivedIssues []DerivedIssue
	inboundInternalLinkCounts, outboundInternalLinkCounts := countInternalLinksByPage(linkFacts)

	for _, pageFact := range pageFacts {
		titleLength := len(strings.TrimSpace(pageFact.Title))
		metaDescriptionLength := len(strings.TrimSpace(pageFact.MetaDescription))
		robotsValue := strings.ToLower(pageFact.Robots)
		outboundInternalLinkCount := outboundInternalLinkCounts[pageFact.URL]
		inboundInternalLinkCount := inboundInternalLinkCounts[pageFact.URL]

		if pageFact.StatusCode >= 400 && pageFact.StatusCode < 500 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "technical", "client_error_status", "Page returned a client error", fmt.Sprintf("Page returned HTTP %d.", pageFact.StatusCode)))
		} else if pageFact.StatusCode >= 500 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "technical", "server_error_status", "Page returned a server error", fmt.Sprintf("Page returned HTTP %d.", pageFact.StatusCode)))
		}
		if titleLength == 0 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "seo", "missing_title", "Page is missing a title", "Add a descriptive <title> tag."))
		} else {
			if titleLength > longTitleCharacterThreshold {
				derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "title_too_long", "Page title is too long", fmt.Sprintf("Title is %d characters (recommended: 30-60).", titleLength)))
			}
			if titleLength < shortTitleCharacterThreshold {
				derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "title_too_short", "Page title is too short", fmt.Sprintf("Title is %d characters (recommended: 30-60).", titleLength)))
			}
		}

		if metaDescriptionLength == 0 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "missing_meta_description", "Page is missing a meta description", "Add a meta description summarizing the page content."))
		} else {
			if metaDescriptionLength > longMetaDescriptionCharacterThreshold {
				derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "meta_description_too_long", "Meta description is too long", fmt.Sprintf("Meta description is %d characters (recommended: 120-160).", metaDescriptionLength)))
			}
			if metaDescriptionLength < shortMetaDescriptionCharacterThreshold {
				derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "meta_description_too_short", "Meta description is too short", fmt.Sprintf("Meta description is %d characters (recommended: 120-160).", metaDescriptionLength)))
			}
		}

		if strings.TrimSpace(pageFact.H1) == "" {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "seo", "missing_h1", "Page is missing an H1", "Add one primary H1 heading to the page."))
		}
		if pageFact.H1Count > 1 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "multiple_h1", "Page has multiple H1 headings", "Keep one primary H1 heading per page."))
		}
		if pageFact.WordCount >= longPageWordCountThreshold && pageFact.H2Count == 0 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "missing_h2_on_long_page", "Long page is missing H2 headings", fmt.Sprintf("Page has %d words but no H2 subheadings.", pageFact.WordCount)))
		}
		if pageFact.WordCount > 0 && pageFact.WordCount < thinContentWordCountThreshold {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "thin_content", "Page content is thin", "Add more useful page content for users and search engines."))
		}

		if strings.TrimSpace(pageFact.CanonicalURL) == "" {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "missing_canonical", "Page is missing a canonical URL", "Add a canonical link element for the preferred page URL."))
		} else if !canonicalMatchesPageURL(pageFact.URL, pageFact.CanonicalURL) {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "canonical_differs", "Canonical URL differs from page URL", fmt.Sprintf("Canonical points to %s.", pageFact.CanonicalURL)))
		}

		if strings.TrimSpace(pageFact.Viewport) == "" {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "seo", "missing_viewport", "Page is missing a viewport meta tag", "Add a viewport meta tag for mobile optimization."))
		}
		if strings.TrimSpace(pageFact.Lang) == "" {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "missing_lang", "Page is missing a language attribute", "Add a lang attribute to the HTML element."))
		}

		if strings.Contains(robotsValue, "noindex") {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "seo", "noindex_page", "Page is marked noindex", "Remove the noindex directive if the page should appear in search results."))
		}
		if strings.Contains(robotsValue, "nofollow") {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "seo", "nofollow_page", "Page is marked nofollow", "Remove the nofollow directive if search engines should follow links on this page."))
		}

		if !hasMeaningfulOGTags(pageFact.OGTags) {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "low", "seo", "missing_og_tags", "Page is missing Open Graph tags", "Add core Open Graph tags for richer sharing previews."))
		}
		if !hasMeaningfulJSONLD(pageFact.JSONLD) {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "seo", "missing_structured_data", "Page is missing structured data", "Add JSON-LD structured data to the page."))
		}

		if pageFact.ImageCount > 0 && pageFact.ImagesWithoutAltCount > 0 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "low", "seo", "images_missing_alt", "Page has images missing alt text", "Add descriptive alt text to meaningful images."))
		}
		if pageFact.ImageCount > 0 && pageFact.ImagesWithoutDimensions > 0 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "low", "seo", "images_missing_dimensions", "Page has images missing dimensions", "Set explicit image width and height attributes where possible."))
		}

		if pageFact.ResponseTimeMs > slowResponseTimeMillisecondsThreshold {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "pagespeed", "slow_response_time", "Page response time is slow", "Reduce server response time for this page."))
		}
		if pageFact.SizeBytes > largePageSizeBytesThreshold {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "high", "pagespeed", "large_page_size", "Page size is large", fmt.Sprintf("Page size is %.1fMB (recommended: under 3MB).", bytesToMegabytes(pageFact.SizeBytes))))
		} else if pageFact.SizeBytes > moderatePageSizeBytesThreshold {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "pagespeed", "moderate_page_size", "Page size is moderately large", fmt.Sprintf("Page size is %.1fMB (recommended: under 1MB).", bytesToMegabytes(pageFact.SizeBytes))))
		}

		if outboundInternalLinkCount == 0 {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "links", "no_internal_links_out", "Page has no internal links out", "Add internal links from this page to other pages on the site."))
		} else if outboundInternalLinkCount <= lowInternalLinksOutThreshold {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "low", "links", "low_internal_links_out", "Page has few internal links out", fmt.Sprintf("Page only links to %d internal page(s).", outboundInternalLinkCount)))
		}
		if pageFact.Depth > 0 && inboundInternalLinkCount <= lowInternalLinksInThreshold {
			derivedIssues = append(derivedIssues, newPageIssue(pageFact, "medium", "links", "low_internal_links_in", "Page has few internal links in", fmt.Sprintf("Page is linked from %d internal page(s).", inboundInternalLinkCount)))
		}
	}

	return derivedIssues
}

// countInternalLinksByPage computes unique inbound and outbound internal link counts per URL.
func countInternalLinksByPage(linkFacts []LinkFact) (map[string]int, map[string]int) {
	outboundInternalLinkSourcesByTargetURL := make(map[string]map[string]struct{})
	inboundInternalLinkTargetsBySourceURL := make(map[string]map[string]struct{})

	for _, linkFact := range linkFacts {
		sourceURL := strings.TrimSpace(linkFact.SourceURL)
		targetURL := strings.TrimSpace(linkFact.TargetURL)
		if sourceURL == "" || targetURL == "" || sourceURL == targetURL {
			continue
		}

		if _, exists := inboundInternalLinkTargetsBySourceURL[sourceURL]; !exists {
			inboundInternalLinkTargetsBySourceURL[sourceURL] = make(map[string]struct{})
		}
		inboundInternalLinkTargetsBySourceURL[sourceURL][targetURL] = struct{}{}

		if _, exists := outboundInternalLinkSourcesByTargetURL[targetURL]; !exists {
			outboundInternalLinkSourcesByTargetURL[targetURL] = make(map[string]struct{})
		}
		outboundInternalLinkSourcesByTargetURL[targetURL][sourceURL] = struct{}{}
	}

	inboundInternalLinkCounts := make(map[string]int, len(outboundInternalLinkSourcesByTargetURL))
	for targetURL, sourceURLs := range outboundInternalLinkSourcesByTargetURL {
		inboundInternalLinkCounts[targetURL] = len(sourceURLs)
	}

	outboundInternalLinkCounts := make(map[string]int, len(inboundInternalLinkTargetsBySourceURL))
	for sourceURL, targetURLs := range inboundInternalLinkTargetsBySourceURL {
		outboundInternalLinkCounts[sourceURL] = len(targetURLs)
	}

	return inboundInternalLinkCounts, outboundInternalLinkCounts
}

// newPageIssue builds one derived issue row from a page fact.
func newPageIssue(pageFact PageFact, severity string, category string, code string, message string, details string) DerivedIssue {
	return DerivedIssue{
		CrawlPageID: pageFact.ID,
		URL:         pageFact.URL,
		Severity:    severity,
		Category:    category,
		Code:        code,
		Message:     message,
		Details:     details,
	}
}

// canonicalMatchesPageURL reports whether the canonical URL is effectively the same as the page URL.
func canonicalMatchesPageURL(pageURL string, canonicalURL string) bool {
	return strings.TrimSpace(pageURL) == strings.TrimSpace(canonicalURL)
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

// bytesToMegabytes converts raw bytes into MB for issue details.
func bytesToMegabytes(sizeBytes int32) float64 {
	return float64(sizeBytes) / 1024 / 1024
}
