package crawler

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeURL resolves and normalizes a candidate URL for crawling.
func NormalizeURL(candidateURL string, baseURL *url.URL) (*url.URL, error) {
	trimmedCandidateURL := strings.TrimSpace(candidateURL)
	if trimmedCandidateURL == "" {
		return nil, fmt.Errorf("url is empty")
	}

	parsedCandidateURL, err := url.Parse(trimmedCandidateURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	resolvedURL := parsedCandidateURL
	if baseURL != nil {
		resolvedURL = baseURL.ResolveReference(parsedCandidateURL)
	}

	if resolvedURL.Scheme == "" {
		return nil, fmt.Errorf("url scheme is empty")
	}

	resolvedURL.Scheme = strings.ToLower(resolvedURL.Scheme)
	if resolvedURL.Scheme != "http" && resolvedURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme: %s", resolvedURL.Scheme)
	}

	if resolvedURL.Host == "" {
		return nil, fmt.Errorf("url host is empty")
	}

	resolvedURL.Host = strings.ToLower(resolvedURL.Host)
	resolvedURL.Fragment = ""

	if resolvedURL.Path == "" {
		resolvedURL.Path = "/"
	}

	return resolvedURL, nil
}

// IsInternalURL reports whether a candidate URL belongs to the root host.
func IsInternalURL(rootURL *url.URL, candidateURL *url.URL) bool {
	if rootURL == nil || candidateURL == nil {
		return false
	}

	return strings.EqualFold(rootURL.Host, candidateURL.Host)
}
