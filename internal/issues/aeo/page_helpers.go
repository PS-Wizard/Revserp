package aeo

import (
	"encoding/json"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func selectSiteIssuePageFact(pageFacts []shared.PageFact) (shared.PageFact, bool) {
	if len(pageFacts) == 0 {
		return shared.PageFact{}, false
	}
	selectedPageFact := pageFacts[0]
	for _, pageFact := range pageFacts[1:] {
		if pageLooksLikeHomepage(pageFact.URL) && !pageLooksLikeHomepage(selectedPageFact.URL) {
			selectedPageFact = pageFact
			continue
		}
		if pageFact.Depth < selectedPageFact.Depth {
			selectedPageFact = pageFact
		}
	}
	return selectedPageFact, true
}

func selectHomepagePageFact(pageFacts []shared.PageFact) (shared.PageFact, bool) {
	for _, pageFact := range pageFacts {
		if pageLooksLikeHomepage(pageFact.URL) {
			return pageFact, true
		}
	}
	return shared.PageFact{}, false
}

func pageLooksLikeHomepage(pageURL string) bool {
	trimmedPageURL := strings.TrimSpace(strings.ToLower(pageURL))
	return countURLPathSegments(trimmedPageURL) == 0
}

func countURLPathSegments(pageURL string) int {
	withoutProtocol := strings.TrimPrefix(strings.TrimPrefix(pageURL, "https://"), "http://")
	pathParts := strings.SplitN(withoutProtocol, "/", 2)
	if len(pathParts) < 2 {
		return 0
	}
	trimmedPath := strings.Trim(pathParts[1], "/")
	if trimmedPath == "" {
		return 0
	}
	return len(strings.Split(trimmedPath, "/"))
}

func looksLikeAboutPage(lowerURL string) bool {
	return strings.Contains(lowerURL, "/about") || strings.Contains(lowerURL, "/company") || strings.Contains(lowerURL, "/team")
}

func looksLikeContactPage(lowerURL string) bool {
	return strings.Contains(lowerURL, "/contact") || strings.Contains(lowerURL, "/support")
}

func looksLikePolicyPage(lowerURL string) bool {
	for _, fragment := range []string{"/privacy", "/terms", "/policy", "/policies"} {
		if strings.Contains(lowerURL, fragment) {
			return true
		}
	}
	return false
}

func isArticleLikePage(pageFact shared.PageFact) bool {
	if hasArticleLikeJSONLDType(pageFact.JSONLD) {
		return true
	}
	if pageFact.WordCount < authorSignalMinimumWordCount {
		return false
	}
	return hasArticleLikeURLPath(pageFact.URL)
}

func hasArticleLikeURLPath(pageURL string) bool {
	for _, editorialPathFragment := range []string{"/blog/", "/article/", "/articles/", "/guides/", "/news/"} {
		if strings.Contains(pageURL, editorialPathFragment) {
			return true
		}
	}
	return false
}

func hasInsecureHTTPURL(pageURL string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(pageURL)), "http://")
}

func isFAQLikePage(pageFact shared.PageFact) bool {
	if strings.Contains(strings.ToLower(pageFact.URL), "/faq") {
		return true
	}
	questionLikeHeadingCount := countQuestionLikeHeadings(pageFact.HeadingOutline)
	if questionLikeHeadingCount >= faqLikeQuestionHeadingThreshold {
		return true
	}
	return strings.Count(pageFact.VisibleText, "?") >= faqLikeQuestionMarkThreshold
}

func countQuestionLikeHeadings(headingOutline []byte) int {
	var parsedOutline []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(headingOutline, &parsedOutline); err != nil {
		return 0
	}
	questionLikeHeadingCount := 0
	for _, heading := range parsedOutline {
		if strings.Contains(heading.Text, "?") {
			questionLikeHeadingCount++
		}
	}
	return questionLikeHeadingCount
}
