package app

import (
	"regexp"
	"strings"
)

var issueDetailURLPattern = regexp.MustCompile(`https?://[^\s,]+`)

func formatDetailTextForWorkbook(details string) string {
	formattedDetails := strings.TrimSpace(details)
	for _, relatedURL := range extractURLsFromText(formattedDetails) {
		formattedDetails = strings.ReplaceAll(formattedDetails, relatedURL, "\n"+relatedURL)
	}
	return strings.TrimSpace(formattedDetails)
}

func extractURLsFromText(value string) []string {
	matches := issueDetailURLPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return nil
	}

	urls := make([]string, 0, len(matches))
	seenURLs := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		normalizedURL := strings.TrimRight(strings.TrimSpace(match), ".,;)")
		if normalizedURL == "" {
			continue
		}
		if _, exists := seenURLs[normalizedURL]; exists {
			continue
		}
		seenURLs[normalizedURL] = struct{}{}
		urls = append(urls, normalizedURL)
	}
	return urls
}
