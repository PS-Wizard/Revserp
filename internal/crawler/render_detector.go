package crawler

import (
	"regexp"
	"strings"
)

const minimumVisibleTextLengthForPlainHTML = 200
const minimumLinkCountForPlainHTML = 3
const minimumScriptCountForShellHTML = 5
const minimumInlineScriptContentLength = 1000
const minimumBodySizeForShellHTML = 10_000
const jsRenderScoreThreshold = 5

var javaScriptShellMarkers = []string{
	`id="__next"`,
	`id="root"`,
	`id="app"`,
	`id="__nuxt"`,
	`id="svelte"`,
	`data-reactroot`,
	`window.__nuxt__`,
	`__next_data__`,
	`ng-app`,
	`ng-version`,
	`data-sveltekit`,
	`astro-island`,
}

var javaScriptFrameworkMarkers = []string{
	"/_next/static/",
	"/_nuxt/",
	"/assets/index-",
	`type="module"`,
	"/build/",
	"data-reactroot",
	"ng-version",
	"data-sveltekit",
}

var javaScriptRequiredMessages = []string{
	"enable javascript",
	"enablejs",
	"requires javascript",
	"javascript is disabled",
	"you need to enable javascript",
	"please enable javascript",
}

var inlineScriptPattern = regexp.MustCompile(`(?is)<script[^>]*>(.*?)</script>`)

// JSRenderDecision holds the render fallback verdict and the reasons behind it.
type JSRenderDecision struct {
	NeedsRender bool
	Reasons     []string
}

// NeedsJSRender reports whether a fetched HTML page looks too shell-like for plain HTTP extraction.
func NeedsJSRender(fetchResult FetchResult, parsedPage *ParsedPage) JSRenderDecision {
	if !strings.Contains(strings.ToLower(fetchResult.ContentType), "text/html") {
		return JSRenderDecision{}
	}

	bodyText := strings.ToLower(string(fetchResult.Body))
	decision := JSRenderDecision{}
	score := 0
	scriptCount := countHTMLTag(fetchResult.Body, "script")
	inlineScriptContentLength := countInlineScriptContentLength(fetchResult.Body)

	visibleTextLength := 0
	linkCount := 0
	title := ""
	if parsedPage != nil {
		visibleTextLength = len(parsedPage.VisibleText)
		linkCount = len(parsedPage.Links)
		title = parsedPage.Title
	}

	if visibleTextLength < minimumVisibleTextLengthForPlainHTML {
		score += 2
		decision.Reasons = append(decision.Reasons, "visible text is sparse")
	}

	if linkCount <= minimumLinkCountForPlainHTML {
		score++
		decision.Reasons = append(decision.Reasons, "link count is sparse")
	}

	if strings.TrimSpace(title) == "" {
		score++
		decision.Reasons = append(decision.Reasons, "title is empty")
	}

	if scriptCount >= minimumScriptCountForShellHTML && visibleTextLength < minimumVisibleTextLengthForPlainHTML && linkCount <= minimumLinkCountForPlainHTML {
		score++
		decision.Reasons = append(decision.Reasons, "html contains many script tags")
	}

	if containsAnySubstring(bodyText, javaScriptShellMarkers) {
		score += 3
		decision.Reasons = append(decision.Reasons, "html contains javascript app shell markers")
	}

	if containsAnySubstring(bodyText, javaScriptFrameworkMarkers) {
		score++
		decision.Reasons = append(decision.Reasons, "html contains javascript framework markers")
	}

	if inlineScriptContentLength >= minimumInlineScriptContentLength && visibleTextLength < minimumVisibleTextLengthForPlainHTML {
		score += 3
		decision.Reasons = append(decision.Reasons, "html contains substantial inline script data")
	}

	if containsAnySubstring(bodyText, javaScriptRequiredMessages) || strings.Contains(strings.ToLower(fetchResult.FinalURL), "enablejs") {
		decision.Reasons = append(decision.Reasons, "html says javascript is required")
		decision.NeedsRender = true
		return decision
	}

	if len(fetchResult.Body) > minimumBodySizeForShellHTML && visibleTextLength < minimumVisibleTextLengthForPlainHTML {
		score += 2
		decision.Reasons = append(decision.Reasons, "html body is large but extracted content is sparse")
	}

	decision.NeedsRender = score >= jsRenderScoreThreshold
	return decision
}

// containsAnySubstring reports whether bodyText contains any provided lowercase markers.
func containsAnySubstring(bodyText string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(bodyText, marker) {
			return true
		}
	}

	return false
}

// countHTMLTag reports how many times a lowercased HTML tag opener appears in a body.
func countHTMLTag(body []byte, tagName string) int {
	if len(body) == 0 || tagName == "" {
		return 0
	}

	lowercaseBody := strings.ToLower(string(body))
	return strings.Count(lowercaseBody, "<"+strings.ToLower(tagName))
}

// countInlineScriptContentLength sums the text content inside inline script tags.
func countInlineScriptContentLength(body []byte) int {
	matches := inlineScriptPattern.FindAllSubmatch(body, -1)
	inlineScriptContentLength := 0
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		inlineScriptContentLength += len(strings.TrimSpace(string(match[1])))
	}
	return inlineScriptContentLength
}

// shouldPreferRenderedPage reports whether a rendered page extracted meaningfully more content than the raw page.
func shouldPreferRenderedPage(rawPage ParsedPage, renderedPage ParsedPage) bool {
	return pageExtractionQualityScore(renderedPage) > pageExtractionQualityScore(rawPage)
}

// pageExtractionQualityScore ranks extracted page usefulness for choosing between raw and rendered HTML.
func pageExtractionQualityScore(parsedPage ParsedPage) int {
	score := 0
	if strings.TrimSpace(parsedPage.Title) != "" {
		score += 2
	}
	if strings.TrimSpace(parsedPage.MetaDescription) != "" {
		score++
	}
	if strings.TrimSpace(parsedPage.H1) != "" {
		score += 2
	}
	if len(parsedPage.VisibleText) >= minimumVisibleTextLengthForPlainHTML {
		score += 3
	} else if len(strings.TrimSpace(parsedPage.VisibleText)) > 0 {
		score++
	}
	if len(parsedPage.Links) > minimumLinkCountForPlainHTML {
		score += 2
	} else if len(parsedPage.Links) > 0 {
		score++
	}
	return score
}
