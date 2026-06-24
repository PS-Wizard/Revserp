package seo

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func newIssue(pageFact shared.PageFact, bucket string, issueType string, severity string, message string, details string) shared.DerivedIssue {
	return shared.NewIssue(pageFact, PillarID, bucket, issueType, severity, message, details)
}

func countInternalLinksByPage(linkFacts []shared.LinkFact) (map[string]int, map[string]int) {
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

func canonicalMatchesPageURL(pageURL string, canonicalURL string) bool {
	return strings.TrimSpace(pageURL) == strings.TrimSpace(canonicalURL)
}

type headingOutlineEntry struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

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
		skippedHeadingLevelTransitions = append(skippedHeadingLevelTransitions, fmt.Sprintf("H%d %q jumps to H%d %q.", previousHeading.Level, previousHeading.Text, currentHeading.Level, currentHeading.Text))
	}
	if len(skippedHeadingLevelTransitions) == 0 {
		return ""
	}
	return strings.Join(skippedHeadingLevelTransitions, " ")
}

func isTooManyImagesOnPage(pageFact shared.PageFact) bool {
	if pageFact.ImageCount < tooManyImagesMinimumImageCount {
		return false
	}
	if pageFact.WordCount <= 0 {
		return true
	}
	return float64(pageFact.WordCount)/float64(pageFact.ImageCount) < float64(tooManyImagesWordsPerImageThreshold)
}

func titleAndH1Mismatch(title string, h1 string) bool {
	normalizedTitle := normalizeDuplicateContentField(title)
	normalizedH1 := normalizeDuplicateContentField(h1)
	if normalizedTitle == "" || normalizedH1 == "" {
		return false
	}
	if normalizedTitle == normalizedH1 || strings.Contains(normalizedTitle, normalizedH1) || strings.Contains(normalizedH1, normalizedTitle) {
		return false
	}
	return calculateFieldSimilarity(normalizedTitle, normalizedH1) < titleH1MismatchSimilarityThreshold
}

func buildPageFactsByURL(pageFacts []shared.PageFact) map[string]shared.PageFact {
	pageFactsByURL := make(map[string]shared.PageFact, len(pageFacts))
	for _, pageFact := range pageFacts {
		pageFactsByURL[strings.TrimSpace(pageFact.URL)] = pageFact
	}
	return pageFactsByURL
}

func pageIsNonIndexable(pageFact shared.PageFact) bool {
	robotsValue := strings.ToLower(strings.TrimSpace(pageFact.Robots))
	if strings.Contains(robotsValue, "noindex") {
		return true
	}
	return pageFact.StatusCode >= 400
}

func canonicalURLIsMalformed(canonicalURL string) bool {
	parsedCanonicalURL, err := url.Parse(strings.TrimSpace(canonicalURL))
	if err != nil {
		return true
	}
	if !parsedCanonicalURL.IsAbs() {
		return true
	}
	return parsedCanonicalURL.Scheme != "http" && parsedCanonicalURL.Scheme != "https"
}

func collectInternalLinkTargetIssues(linkFacts []shared.LinkFact) (map[string][]string, map[string][]string) {
	brokenTargetsBySourceURL := make(map[string]map[string]struct{})
	redirectingTargetsBySourceURL := make(map[string]map[string]struct{})
	for _, linkFact := range linkFacts {
		sourceURL := strings.TrimSpace(linkFact.SourceURL)
		targetURL := strings.TrimSpace(linkFact.TargetURL)
		if sourceURL == "" || targetURL == "" || sourceURL == targetURL {
			continue
		}
		switch {
		case linkFact.TargetStatus >= 400:
			if _, exists := brokenTargetsBySourceURL[sourceURL]; !exists {
				brokenTargetsBySourceURL[sourceURL] = make(map[string]struct{})
			}
			brokenTargetsBySourceURL[sourceURL][targetURL] = struct{}{}
		case linkFact.TargetStatus >= 300 && linkFact.TargetStatus < 400:
			if _, exists := redirectingTargetsBySourceURL[sourceURL]; !exists {
				redirectingTargetsBySourceURL[sourceURL] = make(map[string]struct{})
			}
			redirectingTargetsBySourceURL[sourceURL][targetURL] = struct{}{}
		}
	}
	return flattenURLSetMap(brokenTargetsBySourceURL), flattenURLSetMap(redirectingTargetsBySourceURL)
}

func flattenURLSetMap(urlsByKey map[string]map[string]struct{}) map[string][]string {
	flattened := make(map[string][]string, len(urlsByKey))
	for key, urlSet := range urlsByKey {
		flattenedURLs := make([]string, 0, len(urlSet))
		for targetURL := range urlSet {
			flattenedURLs = append(flattenedURLs, targetURL)
		}
		sort.Strings(flattenedURLs)
		flattened[key] = flattenedURLs
	}
	return flattened
}

func limitURLsForIssueDetails(urls []string) []string {
	if len(urls) <= 3 {
		return urls
	}
	result := make([]string, 3, 4)
	copy(result, urls[:3])
	return append(result, fmt.Sprintf("and %d more", len(urls)-3))
}

func deriveDuplicateTitleIssues(pageFacts []shared.PageFact) []shared.DerivedIssue {
	return deriveDuplicateFieldIssues(pageFacts, func(pageFact shared.PageFact) string {
		return pageFact.Title
	}, "duplicate_title", "high", "Page shares a duplicate title", "Title matches %d other page(s): %s.")
}

func deriveDuplicateMetaDescriptionIssues(pageFacts []shared.PageFact) []shared.DerivedIssue {
	return deriveDuplicateFieldIssues(pageFacts, func(pageFact shared.PageFact) string {
		return pageFact.MetaDescription
	}, "duplicate_meta_description", "medium", "Page shares a duplicate meta description", "Meta description matches %d other page(s): %s.")
}

func deriveDuplicateFieldIssues(pageFacts []shared.PageFact, fieldValue func(shared.PageFact) string, issueType string, severity string, message string, detailsTemplate string) []shared.DerivedIssue {
	pageIndexesByNormalizedField := make(map[string][]int)
	for pageIndex, pageFact := range pageFacts {
		normalizedFieldValue := normalizeDuplicateContentField(fieldValue(pageFact))
		if normalizedFieldValue == "" {
			continue
		}
		pageIndexesByNormalizedField[normalizedFieldValue] = append(pageIndexesByNormalizedField[normalizedFieldValue], pageIndex)
	}

	normalizedFields := make([]string, 0, len(pageIndexesByNormalizedField))
	for nf := range pageIndexesByNormalizedField {
		normalizedFields = append(normalizedFields, nf)
	}
	sort.Strings(normalizedFields)

	var derivedIssues []shared.DerivedIssue
	for _, normalizedField := range normalizedFields {
		pageIndexes := pageIndexesByNormalizedField[normalizedField]
		if len(pageIndexes) < 2 || duplicateGroupLooksLikePurePagination(pageFacts, pageIndexes) {
			continue
		}
		for _, pageIndex := range pageIndexes {
			matchingURLs := collectOtherDuplicateURLs(pageFacts, pageIndexes, pageIndex)
			derivedIssues = append(derivedIssues, newIssue(
				pageFacts[pageIndex],
				"serp_metadata",
				issueType,
				severity,
				message,
				fmt.Sprintf(detailsTemplate, len(matchingURLs), strings.Join(limitURLsForIssueDetails(matchingURLs), ", ")),
			))
		}
	}
	return derivedIssues
}

func duplicateGroupLooksLikePurePagination(pageFacts []shared.PageFact, pageIndexes []int) bool {
	for _, pageIndex := range pageIndexes {
		if !pageURLLooksPaginated(pageFacts[pageIndex].URL) {
			return false
		}
	}
	return true
}

func pageURLLooksPaginated(pageURL string) bool {
	parsedPageURL, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		return false
	}
	path := strings.ToLower(parsedPageURL.Path)
	if strings.Contains(path, "/page/") {
		return true
	}
	pageValue := strings.TrimSpace(parsedPageURL.Query().Get("page"))
	return pageValue != "" && pageValue != "1"
}
