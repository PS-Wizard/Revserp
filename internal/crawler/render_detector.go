package crawler

import (
	"regexp"
	"strings"
)

const minimumVisibleTextLengthForPlainHTML = 200
const minimumLinkCountForPlainHTML = 3
const minimumPageTextLengthForShellHTML = 400
const minimumInlineScriptContentLength = 1000
const minimumBodySizeForShellHTML = 10_000

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

// NeedsJSRender reports whether a fetched HTML page looks like a JavaScript shell
// that plain HTTP extraction cannot read. Sparse visible text alone is never enough:
// short but complete server-rendered pages are common and must not trigger a render.
func NeedsJSRender(fetchResult FetchResult, parsedPage *ParsedPage) JSRenderDecision {
	if !strings.Contains(strings.ToLower(fetchResult.ContentType), "text/html") {
		return JSRenderDecision{}
	}

	bodyText := strings.ToLower(string(fetchResult.Body))
	if containsAnySubstring(bodyText, javaScriptRequiredMessages) || strings.Contains(strings.ToLower(fetchResult.FinalURL), "enablejs") {
		return JSRenderDecision{
			NeedsRender: true,
			Reasons:     []string{"html says javascript is required"},
		}
	}

	pageTextLength := 0
	visibleTextLength := 0
	if parsedPage != nil {
		pageTextLength = parsedPage.PageTextLength
		visibleTextLength = len(parsedPage.VisibleText)
	}

	// A page may need a render only when it looks like an app shell: the HTML
	// carries a shell marker AND almost no body text. The body-text gate is what
	// keeps a short but server-rendered gallery, login or downloads page from
	// paying for a headless render it does not need.
	if !containsAnySubstring(bodyText, javaScriptShellMarkers) || pageTextLength >= minimumPageTextLengthForShellHTML {
		return JSRenderDecision{}
	}

	decision := JSRenderDecision{NeedsRender: true}
	decision.Reasons = append(decision.Reasons, "html contains javascript app shell markers")

	if containsAnySubstring(bodyText, javaScriptFrameworkMarkers) {
		decision.Reasons = append(decision.Reasons, "html contains javascript framework markers")
	}

	visibleTextSparse := visibleTextLength < minimumVisibleTextLengthForPlainHTML
	if visibleTextSparse {
		decision.Reasons = append(decision.Reasons, "visible text is sparse")
	}

	if countInlineScriptContentLength(fetchResult.Body) >= minimumInlineScriptContentLength {
		decision.Reasons = append(decision.Reasons, "html contains substantial inline script data")
	}

	if len(fetchResult.Body) > minimumBodySizeForShellHTML && visibleTextSparse {
		decision.Reasons = append(decision.Reasons, "html body is large but extracted content is sparse")
	}

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
