package crawler

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultWorkerCount         = 4
	defaultMaxDepth            = 2
	defaultFetchTimeoutSeconds = 10
	defaultUserAgent           = "revserp-bot/0.1"
)

// CrawlConfigSnapshot holds the persisted crawl settings for one crawl row.
type CrawlConfigSnapshot struct {
	MaxDepth            int  `json:"max_depth"`
	MaxPages            *int `json:"max_pages,omitempty"`
	FetchTimeoutSeconds int  `json:"fetch_timeout_seconds"`
	EnableJavascript    bool `json:"enable_javascript"`
}

type crawlConfigSnapshotInput struct {
	MaxDepth            *int  `json:"max_depth"`
	MaxPages            *int  `json:"max_pages"`
	FetchTimeoutSeconds *int  `json:"fetch_timeout_seconds"`
	EnableJavascript    *bool `json:"enable_javascript"`
}

// NormalizeConfigSnapshot resolves defaults and validates one crawl config snapshot.
func NormalizeConfigSnapshot(rawConfigSnapshot []byte) (CrawlConfigSnapshot, []byte, error) {
	resolvedSnapshot := CrawlConfigSnapshot{
		MaxDepth:            defaultMaxDepth,
		FetchTimeoutSeconds: defaultFetchTimeoutSeconds,
		EnableJavascript:    false,
	}

	if len(strings.TrimSpace(string(rawConfigSnapshot))) > 0 {
		var input crawlConfigSnapshotInput
		if err := json.Unmarshal(rawConfigSnapshot, &input); err != nil {
			return CrawlConfigSnapshot{}, nil, err
		}

		if input.MaxDepth != nil {
			if *input.MaxDepth < 0 {
				return CrawlConfigSnapshot{}, nil, errors.New("max_depth must be greater than or equal to zero")
			}
			resolvedSnapshot.MaxDepth = *input.MaxDepth
		}

		if input.MaxPages != nil {
			if *input.MaxPages <= 0 {
				return CrawlConfigSnapshot{}, nil, errors.New("max_pages must be greater than zero when provided")
			}
			resolvedSnapshot.MaxPages = input.MaxPages
		}

		if input.FetchTimeoutSeconds != nil {
			if *input.FetchTimeoutSeconds <= 0 {
				return CrawlConfigSnapshot{}, nil, errors.New("fetch_timeout_seconds must be greater than zero")
			}
			resolvedSnapshot.FetchTimeoutSeconds = *input.FetchTimeoutSeconds
		}

		if input.EnableJavascript != nil {
			resolvedSnapshot.EnableJavascript = *input.EnableJavascript
		}
	}

	normalizedSnapshot, err := json.Marshal(resolvedSnapshot)
	if err != nil {
		return CrawlConfigSnapshot{}, nil, err
	}

	return resolvedSnapshot, normalizedSnapshot, nil
}

// ConfigFromBaseURLAndSnapshot builds crawler execution settings from one project root URL and persisted snapshot.
// TODO: expose additional crawl settings via crawl config / user settings.
func ConfigFromBaseURLAndSnapshot(baseURL string, rawConfigSnapshot []byte) (CrawlerConfig, error) {
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return CrawlerConfig{}, err
	}

	if parsedBaseURL.Host == "" {
		return CrawlerConfig{}, errors.New("missing host")
	}

	configSnapshot, _, err := NormalizeConfigSnapshot(rawConfigSnapshot)
	if err != nil {
		return CrawlerConfig{}, err
	}

	maxPages := 0
	if configSnapshot.MaxPages != nil {
		maxPages = *configSnapshot.MaxPages
	}

	return CrawlerConfig{
		AllowedHost:  normalizeHostForScope(parsedBaseURL.Hostname()),
		MaxDepth:     configSnapshot.MaxDepth,
		MaxPages:     maxPages,
		FetchTimeout: time.Duration(configSnapshot.FetchTimeoutSeconds) * time.Second,
		UserAgent:    defaultUserAgent,
	}, nil
}
