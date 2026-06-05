package analyzer

import (
	"bytes"
	"encoding/json"
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
const tooManyImagesMinimumImageCount = 10
const tooManyImagesWordsPerImageThreshold = 50
const authorSignalMinimumWordCount = 300

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
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "technical_seo", "client_error_status", "high", "Page returned a client error", fmt.Sprintf("Page returned HTTP %d.", pageFact.StatusCode)))
		} else if pageFact.StatusCode >= 500 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "technical_seo", "server_error_status", "high", "Page returned a server error", fmt.Sprintf("Page returned HTTP %d.", pageFact.StatusCode)))
		}
		if titleLength == 0 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "serp_metadata", "missing_title", "high", "Page is missing a title", "Add a descriptive <title> tag."))
		} else {
			if titleLength > longTitleCharacterThreshold {
				derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "serp_metadata", "title_too_long", "medium", "Page title is too long", fmt.Sprintf("Title is %d characters (recommended: 30-60).", titleLength)))
			}
			if titleLength < shortTitleCharacterThreshold {
				derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "serp_metadata", "title_too_short", "medium", "Page title is too short", fmt.Sprintf("Title is %d characters (recommended: 30-60).", titleLength)))
			}
		}

		if metaDescriptionLength == 0 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "serp_metadata", "missing_meta_description", "medium", "Page is missing a meta description", "Add a meta description summarizing the page content."))
		} else {
			if metaDescriptionLength > longMetaDescriptionCharacterThreshold {
				derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "serp_metadata", "meta_description_too_long", "medium", "Meta description is too long", fmt.Sprintf("Meta description is %d characters (recommended: 120-160).", metaDescriptionLength)))
			}
			if metaDescriptionLength < shortMetaDescriptionCharacterThreshold {
				derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "serp_metadata", "meta_description_too_short", "medium", "Meta description is too short", fmt.Sprintf("Meta description is %d characters (recommended: 120-160).", metaDescriptionLength)))
			}
		}

		if strings.TrimSpace(pageFact.H1) == "" {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "content_structure", "missing_h1", "high", "Page is missing an H1", "Add one primary H1 heading to the page."))
		}
		if pageFact.H1Count > 1 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "content_structure", "multiple_h1", "medium", "Page has multiple H1 headings", "Keep one primary H1 heading per page."))
		}
		if pageFact.WordCount >= longPageWordCountThreshold && pageFact.H2Count == 0 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "content_structure", "missing_h2_on_long_page", "medium", "Long page is missing H2 headings", fmt.Sprintf("Page has %d words but no H2 subheadings.", pageFact.WordCount)))
		}
		if skippedHeadingLevelDetails := buildSkippedHeadingLevelDetails(pageFact.HeadingOutline); skippedHeadingLevelDetails != "" {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "content_structure", "skipped_heading_levels", "medium", "Page skips heading levels", skippedHeadingLevelDetails))
		}
		if pageFact.WordCount > 0 && pageFact.WordCount < thinContentWordCountThreshold {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "content_quality", "thin_content", "medium", "Page content is thin", "Add more useful page content for users and search engines."))
		}

		if strings.TrimSpace(pageFact.CanonicalURL) == "" {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "indexability", "missing_canonical", "medium", "Page is missing a canonical URL", "Add a canonical link element for the preferred page URL."))
		} else if !canonicalMatchesPageURL(pageFact.URL, pageFact.CanonicalURL) {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "indexability", "canonical_differs", "medium", "Canonical URL differs from page URL", fmt.Sprintf("Canonical points to %s.", pageFact.CanonicalURL)))
		}

		if strings.TrimSpace(pageFact.Viewport) == "" {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "technical_seo", "missing_viewport", "high", "Page is missing a viewport meta tag", "Add a viewport meta tag for mobile optimization."))
		}
		if strings.TrimSpace(pageFact.Lang) == "" {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "technical_seo", "missing_lang", "medium", "Page is missing a language attribute", "Add a lang attribute to the HTML element."))
		}

		if strings.Contains(robotsValue, "noindex") {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "indexability", "noindex_page", "high", "Page is marked noindex", "Remove the noindex directive if the page should appear in search results."))
		}
		if strings.Contains(robotsValue, "nofollow") {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "indexability", "nofollow_page", "medium", "Page is marked nofollow", "Remove the nofollow directive if search engines should follow links on this page."))
		}

		if !hasMeaningfulOGTags(pageFact.OGTags) {
			derivedIssues = append(derivedIssues, newAEOIssue(pageFact, "experience", "missing_og_tags", "low", "Page is missing Open Graph tags", "Add core Open Graph tags for richer sharing previews."))
		}
		if !hasMeaningfulJSONLD(pageFact.JSONLD) {
			derivedIssues = append(derivedIssues, newAEOIssue(pageFact, "answerability", "missing_structured_data", "high", "Page is missing structured data", "Add JSON-LD structured data to the page."))
		}
		if isArticleLikePage(pageFact) && !hasAuthorSignal(pageFact) {
			derivedIssues = append(derivedIssues, newAEOIssue(pageFact, "expertise", "missing_author_signal", "low", "Page is missing an author signal", "Article-like page does not expose author attribution via metadata or structured data."))
		}

		if pageFact.ImageCount > 0 && pageFact.ImagesWithoutAltCount > 0 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "media_optimization", "images_missing_alt", "low", "Page has images missing alt text", "Add descriptive alt text to meaningful images."))
		}
		if pageFact.ImageCount > 0 && pageFact.ImagesWithoutDimensions > 0 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "media_optimization", "images_missing_dimensions", "low", "Page has images missing dimensions", "Set explicit image width and height attributes where possible."))
		}
		if isTooManyImagesOnPage(pageFact) {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "media_optimization", "too_many_images_on_page", "low", "Page may have too many images for its content length", fmt.Sprintf("Page has %d images and %d words, which is fewer than %d words per image.", pageFact.ImageCount, pageFact.WordCount, tooManyImagesWordsPerImageThreshold)))
		}

		if pageFact.ResponseTimeMs > slowResponseTimeMillisecondsThreshold {
			derivedIssues = append(derivedIssues, newPageSpeedIssue(pageFact, "server_responsiveness", "slow_response_time", "medium", "Page response time is slow", fmt.Sprintf("Page response time is %dms.", pageFact.ResponseTimeMs)))
		}
		if pageFact.SizeBytes > largePageSizeBytesThreshold {
			derivedIssues = append(derivedIssues, newPageSpeedIssue(pageFact, "page_weight", "large_page_size", "high", "Page size is large", fmt.Sprintf("Page size is %.1fMB (recommended: under 3MB).", bytesToMegabytes(pageFact.SizeBytes))))
		} else if pageFact.SizeBytes > moderatePageSizeBytesThreshold {
			derivedIssues = append(derivedIssues, newPageSpeedIssue(pageFact, "page_weight", "moderate_page_size", "medium", "Page size is moderately large", fmt.Sprintf("Page size is %.1fMB (recommended: under 1MB).", bytesToMegabytes(pageFact.SizeBytes))))
		}

		if outboundInternalLinkCount == 0 {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "internal_linking", "no_internal_links_out", "medium", "Page has no internal links out", "Add internal links from this page to other pages on the site."))
		} else if outboundInternalLinkCount <= lowInternalLinksOutThreshold {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "internal_linking", "low_internal_links_out", "low", "Page has few internal links out", fmt.Sprintf("Page only links to %d internal page(s).", outboundInternalLinkCount)))
		}
		if pageFact.Depth > 0 && inboundInternalLinkCount <= lowInternalLinksInThreshold {
			derivedIssues = append(derivedIssues, newSEOIssue(pageFact, "internal_linking", "low_internal_links_in", "medium", "Page has few internal links in", fmt.Sprintf("Page is linked from %d internal page(s).", inboundInternalLinkCount)))
		}
	}

	derivedIssues = append(derivedIssues, deriveDuplicateContentIssues(pageFacts)...)

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

// newSEOIssue builds one SEO derived issue row from a page fact.
func newSEOIssue(pageFact PageFact, bucket string, issueType string, severity string, message string, details string) DerivedIssue {
	return newPageIssue(pageFact, "seo", bucket, issueType, severity, message, details)
}

// newAEOIssue builds one AEO derived issue row from a page fact.
func newAEOIssue(pageFact PageFact, bucket string, issueType string, severity string, message string, details string) DerivedIssue {
	return newPageIssue(pageFact, "aeo", bucket, issueType, severity, message, details)
}

// newPageSpeedIssue builds one PageSpeed derived issue row from a page fact.
func newPageSpeedIssue(pageFact PageFact, bucket string, issueType string, severity string, message string, details string) DerivedIssue {
	return newPageIssue(pageFact, "pagespeed", bucket, issueType, severity, message, details)
}

// newPageIssue builds one derived issue row from a page fact.
func newPageIssue(pageFact PageFact, pillar string, bucket string, issueType string, severity string, message string, details string) DerivedIssue {
	return DerivedIssue{
		CrawlPageID: pageFact.ID,
		URL:         pageFact.URL,
		Pillar:      pillar,
		Bucket:      bucket,
		IssueType:   issueType,
		Severity:    severity,
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

type headingOutlineEntry struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// buildSkippedHeadingLevelDetails returns one detail string when a page's heading outline skips levels.
func buildSkippedHeadingLevelDetails(headingOutline []byte) string {
	var parsedHeadingOutline []headingOutlineEntry
	if err := json.Unmarshal(headingOutline, &parsedHeadingOutline); err != nil {
		return ""
	}
	if len(parsedHeadingOutline) < 2 {
		return ""
	}

	var skippedHeadingLevelTransitions []string
	for headingIndex := 1; headingIndex < len(parsedHeadingOutline); headingIndex++ {
		previousHeading := parsedHeadingOutline[headingIndex-1]
		currentHeading := parsedHeadingOutline[headingIndex]
		if previousHeading.Level <= 0 || currentHeading.Level <= 0 {
			continue
		}
		if currentHeading.Level <= previousHeading.Level+1 {
			continue
		}

		skippedHeadingLevelTransitions = append(skippedHeadingLevelTransitions, fmt.Sprintf("H%d \"%s\" jumps to H%d \"%s\".", previousHeading.Level, previousHeading.Text, currentHeading.Level, currentHeading.Text))
	}

	if len(skippedHeadingLevelTransitions) == 0 {
		return ""
	}

	return strings.Join(skippedHeadingLevelTransitions, " ")
}

// isTooManyImagesOnPage reports whether a page is overly image-dense for its content length.
func isTooManyImagesOnPage(pageFact PageFact) bool {
	if pageFact.ImageCount < tooManyImagesMinimumImageCount {
		return false
	}
	if pageFact.WordCount <= 0 {
		return true
	}

	return pageFact.WordCount/pageFact.ImageCount < tooManyImagesWordsPerImageThreshold
}

// isArticleLikePage reports whether a page looks like editorial content that should expose authorship.
func isArticleLikePage(pageFact PageFact) bool {
	if hasArticleLikeJSONLDType(pageFact.JSONLD) {
		return true
	}
	if pageFact.WordCount < authorSignalMinimumWordCount {
		return false
	}

	return hasArticleLikeURLPath(pageFact.URL)
}

// hasAuthorSignal reports whether a page exposes an author signal in persisted facts.
func hasAuthorSignal(pageFact PageFact) bool {
	if strings.TrimSpace(pageFact.Author) != "" {
		return true
	}

	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		if strings.TrimSpace(ogTags["og:author"]) != "" || strings.TrimSpace(ogTags["article:author"]) != "" {
			return true
		}
	}

	return false
}

// hasArticleLikeJSONLDType reports whether persisted JSON-LD includes article-like schema types.
func hasArticleLikeJSONLDType(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}

	return hasArticleLikeJSONLDTypeValue(parsedJSONLD)
}

// hasArticleLikeJSONLDTypeValue walks one JSON-LD value looking for article-like schema types.
func hasArticleLikeJSONLDTypeValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if rawGraphEntries, ok := typedValue["@graph"].([]any); ok {
			for _, graphEntry := range rawGraphEntries {
				if hasArticleLikeJSONLDTypeValue(graphEntry) {
					return true
				}
			}
		}

		return hasArticleLikeSchemaType(typedValue["@type"])
	case []any:
		for _, entry := range typedValue {
			if hasArticleLikeJSONLDTypeValue(entry) {
				return true
			}
		}
	}

	return false
}

// hasArticleLikeSchemaType reports whether one JSON-LD @type is article-like.
func hasArticleLikeSchemaType(value any) bool {
	switch typedValue := value.(type) {
	case string:
		return isArticleLikeSchemaTypeName(typedValue)
	case []any:
		for _, entry := range typedValue {
			typeName, ok := entry.(string)
			if ok && isArticleLikeSchemaTypeName(typeName) {
				return true
			}
		}
	}

	return false
}

// isArticleLikeSchemaTypeName reports whether one schema type implies authored editorial content.
func isArticleLikeSchemaTypeName(typeName string) bool {
	switch strings.TrimSpace(typeName) {
	case "Article", "BlogPosting", "NewsArticle", "TechArticle":
		return true
	default:
		return false
	}
}

// hasArticleLikeURLPath reports whether one page URL path looks editorial.
func hasArticleLikeURLPath(pageURL string) bool {
	for _, editorialPathFragment := range []string{"/blog/", "/article/", "/articles/", "/guides/", "/news/"} {
		if strings.Contains(pageURL, editorialPathFragment) {
			return true
		}
	}

	return false
}
