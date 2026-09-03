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
	RequestDelayMs      *int `json:"request_delay_ms,omitempty"`
	RequestJitterMs     *int `json:"request_jitter_ms,omitempty"`
	// ForceFullCrawl disables conditional requests for this crawl, refetching and
	// reparsing every page even when the origin reports it unchanged.
	ForceFullCrawl bool `json:"force_full_crawl,omitempty"`
	// HonourRobotsTxt gates the page-crawl loop on the site's robots.txt:
	// disallowed URLs are not fetched and produce no crawl_pages rows.
	HonourRobotsTxt bool `json:"honour_robots_txt,omitempty"`
}

type crawlConfigSnapshotInput struct {
	MaxDepth            *int  `json:"max_depth"`
	MaxPages            *int  `json:"max_pages"`
	FetchTimeoutSeconds *int  `json:"fetch_timeout_seconds"`
	RequestDelayMs      *int  `json:"request_delay_ms"`
	RequestJitterMs     *int  `json:"request_jitter_ms"`
	ForceFullCrawl      *bool `json:"force_full_crawl"`
	HonourRobotsTxt     *bool `json:"honour_robots_txt"`
}

// NormalizeConfigSnapshot resolves defaults and validates one crawl config snapshot.
func NormalizeConfigSnapshot(rawConfigSnapshot []byte) (CrawlConfigSnapshot, []byte, error) {
	resolvedSnapshot := CrawlConfigSnapshot{
		MaxDepth:            defaultMaxDepth,
		FetchTimeoutSeconds: defaultFetchTimeoutSeconds,
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

		if input.RequestDelayMs != nil {
			if *input.RequestDelayMs <= 0 {
				return CrawlConfigSnapshot{}, nil, errors.New("request_delay_ms must be greater than zero when provided")
			}
			resolvedSnapshot.RequestDelayMs = input.RequestDelayMs
		}

		if input.RequestJitterMs != nil {
			if *input.RequestJitterMs <= 0 {
				return CrawlConfigSnapshot{}, nil, errors.New("request_jitter_ms must be greater than zero when provided")
			}
			resolvedSnapshot.RequestJitterMs = input.RequestJitterMs
		}

		if input.ForceFullCrawl != nil {
			resolvedSnapshot.ForceFullCrawl = *input.ForceFullCrawl
		}

		if input.HonourRobotsTxt != nil {
			resolvedSnapshot.HonourRobotsTxt = *input.HonourRobotsTxt
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

	var requestDelay time.Duration
	if configSnapshot.RequestDelayMs != nil {
		requestDelay = time.Duration(*configSnapshot.RequestDelayMs) * time.Millisecond
	}

	var requestJitter time.Duration
	if configSnapshot.RequestJitterMs != nil {
		requestJitter = time.Duration(*configSnapshot.RequestJitterMs) * time.Millisecond
	}

	return CrawlerConfig{
		AllowedHost:    normalizeHostForScope(parsedBaseURL.Hostname()),
		MaxDepth:       configSnapshot.MaxDepth,
		MaxPages:       maxPages,
		FetchTimeout:   time.Duration(configSnapshot.FetchTimeoutSeconds) * time.Second,
		RequestDelay:   requestDelay,
		RequestJitter:  requestJitter,
		UserAgent:      defaultUserAgent,
		ForceFullCrawl: configSnapshot.ForceFullCrawl,
		HonourRobotsTxt: configSnapshot.HonourRobotsTxt,
	}, nil
}
