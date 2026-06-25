package crawler

import "time"

// CrawlerConfig holds crawl execution settings.
type CrawlerConfig struct {
	AllowedHost   string
	MaxDepth      int
	MaxPages      int
	FetchTimeout  time.Duration
	RequestDelay  time.Duration
	RequestJitter time.Duration
	UserAgent     string
}

// CrawlJob represents one URL scheduled for crawling.
type CrawlJob struct {
	URL   string
	Depth int
}

// FetchResult holds the outcome of one HTTP fetch.
type FetchResult struct {
	FinalURL     string
	StatusCode   int
	ContentType  string
	RetryAfter   string // raw Retry-After header value; non-empty only on throttled responses
	Body         []byte
	ResponseTime time.Duration
	ResponseSize int
	FetchError   error
}

// CrawlResult holds the processed outcome of one crawl job.
type CrawlResult struct {
	Job                CrawlJob
	Fetch              FetchResult
	ParsedPage         *ParsedPage
	JavascriptRendered bool
	ProcessErr         error
}
