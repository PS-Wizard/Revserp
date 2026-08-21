package crawler

import "unicode"

// countParsedLinks returns internal and external link counts for one parsed page.
func countParsedLinks(parsedPage *ParsedPage) (int, int) {
	if parsedPage == nil {
		return 0, 0
	}

	internalLinkCount := 0
	externalLinkCount := 0
	for _, parsedLink := range parsedPage.Links {
		if parsedLink.IsInternal {
			internalLinkCount++
			continue
		}

		externalLinkCount++
	}

	return internalLinkCount, externalLinkCount
}

// extractPageTitle returns the parsed page title when available.
func extractPageTitle(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Title
}

// extractMetaDescription returns the parsed meta description when available.
func extractMetaDescription(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.MetaDescription
}

// extractPageAuthor returns the parsed author signal when available.
func extractPageAuthor(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Author
}

// extractPageLang returns the parsed language when available.
func extractPageLang(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Lang
}

// extractPageViewport returns the parsed viewport value when available.
func extractPageViewport(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Viewport
}

// extractPageRobots returns the parsed robots value when available.
func extractPageRobots(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.Robots
}

// extractPageH1 returns the parsed first h1 when available.
func extractPageH1(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.H1
}

// extractPageH1Count returns the parsed h1 count when available.
func extractPageH1Count(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.H1Count
}

// extractPageOGTags returns parsed Open Graph tags when available.
func extractPageOGTags(parsedPage *ParsedPage) map[string]string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.OGTags
}

// extractPageJSONLDBlocks returns parsed JSON-LD blocks when available.
func extractPageJSONLDBlocks(parsedPage *ParsedPage) []string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.JSONLDBlocks
}

// extractPageImageCount returns the parsed image count when available.
func extractPageImageCount(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.ImageCount
}

// extractPageImagesWithoutAltCount returns the parsed missing-alt image count when available.
func extractPageImagesWithoutAltCount(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.ImagesWithoutAltCount
}

// extractPageImagesWithoutDimensions returns the parsed missing-dimensions image count when available.
func extractPageImagesWithoutDimensions(parsedPage *ParsedPage) int {
	if parsedPage == nil {
		return 0
	}

	return parsedPage.ImagesWithoutDimensions
}

// extractH2Headings returns parsed h2 headings when available.
func extractH2Headings(parsedPage *ParsedPage) []string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.H2Headings
}

// extractH3Headings returns parsed h3 headings when available.
func extractH3Headings(parsedPage *ParsedPage) []string {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.H3Headings
}

// extractParsedHeadingOutline returns parsed heading outline when available.
func extractParsedHeadingOutline(parsedPage *ParsedPage) []ParsedHeading {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.HeadingOutline
}

// extractPageContentBlocks returns parsed content blocks when available.
func extractPageContentBlocks(parsedPage *ParsedPage) []ParsedBlock {
	if parsedPage == nil {
		return nil
	}

	return parsedPage.ContentBlocks
}

// extractPageVisibleText returns parsed visible body text when available.
func extractPageVisibleText(parsedPage *ParsedPage) string {
	if parsedPage == nil {
		return ""
	}

	return parsedPage.VisibleText
}

// isResultInternal reports whether a fetched result URL belongs to the crawl root host.
func isResultInternal(rootURL string, result CrawlResult) bool {
	normalizedRootURL, rootErr := NormalizeURL(rootURL, nil)
	if rootErr != nil {
		return false
	}

	normalizedResultURL, resultErr := NormalizeURL(result.Fetch.FinalURL, nil)
	if resultErr != nil {
		return false
	}

	return IsInternalURL(normalizedRootURL, normalizedResultURL)
}

// countWords returns a simple whitespace-based word count.
func countWords(value string) int {
	wordCount := 0
	insideWord := false

	for _, character := range value {
		if unicode.IsSpace(character) {
			insideWord = false
			continue
		}

		if insideWord {
			continue
		}

		insideWord = true
		wordCount++
	}

	return wordCount
}
