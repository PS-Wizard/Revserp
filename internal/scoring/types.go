package scoring

// CrawlPageSignal holds the persisted page fields used for crawl scoring.
type CrawlPageSignal struct {
	URL            string
	StatusCode     int32
	ContentType    string
	WordCount      int32
	ResponseTimeMs int32
	SizeBytes      int32
	OGTags         []byte
	JSONLD         []byte
}

// CrawlIssueSignal holds the persisted issue fields used for crawl scoring.
type CrawlIssueSignal struct {
	URL       string
	Pillar    string
	Bucket    string
	Severity  string
	IssueType string
	Message   string
	Details   string
}

// CrawlScores holds the persisted top-level crawl scores.
type CrawlScores struct {
	SEOScore       int32
	AEOScore       int32
	PageSpeedScore int32
	OverallScore   int32
}
