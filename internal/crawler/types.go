package crawler

import "time"

// CrawlerConfig holds crawl execution settings.
type CrawlerConfig struct {
	AllowedHost    string
	MaxDepth       int
	MaxPages       int
	FetchTimeout   time.Duration
	RequestDelay   time.Duration
	RequestJitter  time.Duration
	UserAgent      string
	ForceFullCrawl bool
}

// CrawlJob represents one URL scheduled for crawling.
// ETag and LastModified carry the previous completed crawl's validators for
// this URL, when one exists, so the fetch can be made conditional.
type CrawlJob struct {
	URL          string
	Depth        int
	ETag         string
	LastModified string
}

// FetchResult holds the outcome of one HTTP fetch.
type FetchResult struct {
	FinalURL     string
	StatusCode   int
	ContentType  string
	RetryAfter   string // raw Retry-After header value; non-empty only on throttled responses
	ETag         string // raw ETag header value, stored opaquely for later conditional requests
	LastModified string // raw Last-Modified header value, stored opaquely (never parsed)
	NotModified  bool   // origin answered 304: the body is unchanged since the sent validator
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
	// NotModified marks a page the origin confirmed unchanged via a conditional
	// request. Such a result has no ParsedPage: its facts are copied forward
	// from the baseline crawl instead of being re-derived from a fresh parse.
	NotModified bool
	ProcessErr  error
}
