package seo

import (
	"fmt"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveIssues builds SEO issues from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, linkFacts []shared.LinkFact) []shared.DerivedIssue {
	derivedIssues, _ := DeriveIssuesWithDuplicateEvidence(pageFacts, linkFacts)
	return derivedIssues
}

// DeriveIssuesWithDuplicateEvidence also returns structured duplicate groups
// and relations produced during the same derivation pass.
func DeriveIssuesWithDuplicateEvidence(pageFacts []shared.PageFact, linkFacts []shared.LinkFact) ([]shared.DerivedIssue, DuplicateEvidence) {
	var derivedIssues []shared.DerivedIssue
	inboundInternalLinkCounts, outboundInternalLinkCounts := countInternalLinksByPage(linkFacts)
	brokenInternalLinkTargetsBySourceURL, redirectingInternalLinkTargetsBySourceURL := collectInternalLinkTargetIssues(linkFacts)
	pageFactsByURL := buildPageFactsByURL(pageFacts)

	for _, pageFact := range pageFacts {
		titleLength := len(strings.TrimSpace(pageFact.Title))
		metaDescriptionLength := len(strings.TrimSpace(pageFact.MetaDescription))
		robotsValue := strings.ToLower(pageFact.Robots)
		outboundInternalLinkCount := outboundInternalLinkCounts[pageFact.URL]
		inboundInternalLinkCount := inboundInternalLinkCounts[pageFact.URL]
		visibleTextWordCount := len(strings.Fields(pageFact.VisibleText))

		// Status issues are not derived here: an unhealthy page never reaches this
		// loop. DeriveBrokenPageIssues owns them, so a broken page yields one issue
		// instead of the full content set. See issues.DeriveIssues for the split.
		if titleLength == 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "serp_metadata", "missing_title", "high", "Page is missing a title", "Add a descriptive <title> tag."))
		} else {
			if titleLength > longTitleCharacterThreshold {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "serp_metadata", "title_too_long", "medium", "Page title is too long", fmt.Sprintf("Title is %d characters (recommended: 30-60).", titleLength)))
			}
			if titleLength < shortTitleCharacterThreshold {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "serp_metadata", "title_too_short", "medium", "Page title is too short", fmt.Sprintf("Title is %d characters (recommended: 30-60).", titleLength)))
			}
		}

		if metaDescriptionLength == 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "serp_metadata", "missing_meta_description", "medium", "Page is missing a meta description", "Add a meta description summarizing the page content."))
		} else {
			if metaDescriptionLength > longMetaDescriptionCharacterThreshold {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "serp_metadata", "meta_description_too_long", "medium", "Meta description is too long", fmt.Sprintf("Meta description is %d characters (recommended: 120-160).", metaDescriptionLength)))
			}
			if metaDescriptionLength < shortMetaDescriptionCharacterThreshold {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "serp_metadata", "meta_description_too_short", "medium", "Meta description is too short", fmt.Sprintf("Meta description is %d characters (recommended: 120-160).", metaDescriptionLength)))
			}
		}

		if strings.TrimSpace(pageFact.H1) == "" {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_structure", "missing_h1", "high", "Page is missing an H1", "Add one primary H1 heading to the page."))
		}
		if pageFact.H1Count > 1 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_structure", "multiple_h1", "medium", "Page has multiple H1 headings", "Keep one primary H1 heading per page."))
		}
		if titleAndH1Mismatch(pageFact.Title, pageFact.H1) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_structure", "title_h1_mismatch", "medium", "Page title and H1 do not align", fmt.Sprintf("Title %q does not closely match H1 %q.", strings.TrimSpace(pageFact.Title), strings.TrimSpace(pageFact.H1))))
		}
		if pageFact.WordCount >= longPageWordCountThreshold && pageFact.H2Count == 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_structure", "missing_h2_on_long_page", "medium", "Long page is missing H2 headings", fmt.Sprintf("Page has %d words but no H2 subheadings.", pageFact.WordCount)))
		}
		if skippedHeadingLevelDetails := buildSkippedHeadingLevelDetails(pageFact.HeadingOutline); skippedHeadingLevelDetails != "" {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_structure", "skipped_heading_levels", "medium", "Page skips heading levels", skippedHeadingLevelDetails))
		}
		if pageFact.WordCount > 0 && pageFact.WordCount < thinContentWordCountThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_quality", "thin_content", "medium", "Page content is thin", "Add more useful page content for users and search engines."))
		}
		if visibleTextWordCount < nearEmptyVisibleContentWordThreshold {
			severity := "medium"
			message := "Page has near-empty visible content"
			details := fmt.Sprintf("Page only has %d visible word(s).", visibleTextWordCount)
			if visibleTextWordCount == 0 {
				severity = "high"
				message = "Page has empty visible content"
				details = "Page has no meaningful visible text content."
			}
			derivedIssues = append(derivedIssues, newIssue(pageFact, "content_quality", "near_empty_visible_content", severity, message, details))
		}

		if strings.TrimSpace(pageFact.CanonicalURL) == "" {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "indexability", "missing_canonical", "medium", "Page is missing a canonical URL", "Add a canonical link element for the preferred page URL."))
		} else {
			if canonicalURLIsMalformed(pageFact.CanonicalURL) {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "indexability", "malformed_canonical", "high", "Canonical URL is malformed", fmt.Sprintf("Canonical value %q is not a valid absolute HTTP URL.", pageFact.CanonicalURL)))
			} else {
				if !canonicalMatchesPageURL(pageFact.URL, pageFact.CanonicalURL) {
					derivedIssues = append(derivedIssues, newIssue(pageFact, "indexability", "canonical_differs", "medium", "Canonical URL differs from page URL", fmt.Sprintf("Canonical points to %s.", pageFact.CanonicalURL)))
				}
				if canonicalTargetPageFact, hasCanonicalTargetPageFact := pageFactsByURL[strings.TrimSpace(pageFact.CanonicalURL)]; hasCanonicalTargetPageFact && pageIsNonIndexable(canonicalTargetPageFact) {
					derivedIssues = append(derivedIssues, newIssue(pageFact, "indexability", "canonical_points_to_non_indexable_page", "high", "Canonical points to a non-indexable page", fmt.Sprintf("Canonical target %s is non-indexable.", canonicalTargetPageFact.URL)))
				}
			}
		}

		if strings.TrimSpace(pageFact.Viewport) == "" {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "technical_seo", "missing_viewport", "high", "Page is missing a viewport meta tag", "Add a viewport meta tag for mobile optimization."))
		}
		if strings.TrimSpace(pageFact.Lang) == "" {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "technical_seo", "missing_lang", "medium", "Page is missing a language attribute", "Add a lang attribute to the HTML element."))
		}
		if strings.Contains(robotsValue, "noindex") {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "indexability", "noindex_page", "high", "Page is marked noindex", "Remove the noindex directive if the page should appear in search results."))
		}
		if strings.Contains(robotsValue, "nofollow") {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "indexability", "nofollow_page", "medium", "Page is marked nofollow", "Remove the nofollow directive if search engines should follow links on this page."))
		}

		if pageFact.ImageCount > 0 && pageFact.ImagesWithoutAltCount > 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "media_optimization", "images_missing_alt", "low", "Page has images missing alt text", "Add descriptive alt text to meaningful images."))
		}
		if pageFact.ImageCount > 0 && pageFact.ImagesWithoutDimensions > 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "media_optimization", "images_missing_dimensions", "low", "Page has images missing dimensions", "Set explicit image width and height attributes where possible."))
		}
		if isTooManyImagesOnPage(pageFact) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "media_optimization", "too_many_images_on_page", "low", "Page may have too many images for its content length", fmt.Sprintf("Page has %d images and %d words, which is fewer than %d words per image.", pageFact.ImageCount, pageFact.WordCount, tooManyImagesWordsPerImageThreshold)))
		}

		if outboundInternalLinkCount == 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "no_internal_links_out", "medium", "Page has no internal links out", "Add internal links from this page to other pages on the site."))
		} else if outboundInternalLinkCount <= lowInternalLinksOutThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "low_internal_links_out", "low", "Page has few internal links out", fmt.Sprintf("Page only links to %d internal page(s).", outboundInternalLinkCount)))
		}
		if pageFact.Depth > 0 && inboundInternalLinkCount == 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "orphan_like_page", "high", "Page appears orphan-like", "Page has no discovered internal links pointing to it."))
		} else if pageFact.Depth > 0 && inboundInternalLinkCount <= lowInternalLinksInThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "low_internal_links_in", "medium", "Page has few internal links in", fmt.Sprintf("Page is linked from %d internal page(s).", inboundInternalLinkCount)))
		}
		if pageFact.Depth >= veryDeepPageDepthThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "very_deep_page", "medium", "Page is very deep in the crawl", fmt.Sprintf("Page was discovered at crawl depth %d.", pageFact.Depth)))
		}
		if brokenTargetURLs := brokenInternalLinkTargetsBySourceURL[pageFact.URL]; len(brokenTargetURLs) > 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "internal_links_to_broken_pages", "high", "Page links to broken internal targets", fmt.Sprintf("Page links to %d broken internal target(s): %s.", len(brokenTargetURLs), strings.Join(limitURLsForIssueDetails(brokenTargetURLs), ", "))))
		}
		if redirectingTargetURLs := redirectingInternalLinkTargetsBySourceURL[pageFact.URL]; len(redirectingTargetURLs) > 0 {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "internal_linking", "internal_links_to_redirects", "medium", "Page links to redirecting internal targets", fmt.Sprintf("Page links to %d redirecting internal target(s): %s.", len(redirectingTargetURLs), strings.Join(limitURLsForIssueDetails(redirectingTargetURLs), ", "))))
		}
	}

	EnrichPageFactsWithContentFingerprints(pageFacts)
	titleIssues, titleGroups := deriveDuplicateTitleIssuesWithEvidence(pageFacts)
	metaIssues, metaGroups := deriveDuplicateMetaDescriptionIssuesWithEvidence(pageFacts)
	contentIssues, evidence := deriveDuplicateContentIssuesWithEvidence(pageFacts)
	derivedIssues = append(derivedIssues, titleIssues...)
	derivedIssues = append(derivedIssues, metaIssues...)
	derivedIssues = append(derivedIssues, contentIssues...)
	groups := make([]DuplicateGroup, 0, len(titleGroups)+len(metaGroups)+len(evidence.Groups))
	groups = append(groups, titleGroups...)
	groups = append(groups, metaGroups...)
	groups = append(groups, evidence.Groups...)
	evidence.Groups = groups
	return derivedIssues, evidence
}
