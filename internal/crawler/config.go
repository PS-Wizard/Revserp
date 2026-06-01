package crawler

import (
	"errors"
	"net/url"
	"time"
)

const DefaultWorkerCount = 4

// DefaultConfigFromBaseURL builds the current hardcoded crawler settings for one project root URL.
// TODO: expose crawl settings via crawl config / user settings.
func DefaultConfigFromBaseURL(baseURL string) (CrawlerConfig, error) {
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return CrawlerConfig{}, err
	}

	if parsedBaseURL.Host == "" {
		return CrawlerConfig{}, errors.New("missing host")
	}

	return CrawlerConfig{
		AllowedHost:  parsedBaseURL.Host,
		MaxDepth:     2,
		MaxPages:     100,
		FetchTimeout: 10 * time.Second,
		UserAgent:    "revserp-bot/0.1",
	}, nil
}
