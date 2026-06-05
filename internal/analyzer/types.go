package analyzer

import "github.com/jackc/pgx/v5/pgtype"

// PageFact holds the persisted page fields used for issue derivation.
type PageFact struct {
	ID                      pgtype.UUID
	URL                     string
	Depth                   int32
	Title                   string
	MetaDescription         string
	Author                  string
	H1                      string
	H1Count                 int32
	H2Count                 int32
	WordCount               int32
	VisibleText             string
	CanonicalURL            string
	Viewport                string
	Lang                    string
	Robots                  string
	StatusCode              int32
	SizeBytes               int32
	ImageCount              int32
	ImagesWithoutAltCount   int32
	ImagesWithoutDimensions int32
	ExternalLinks           int32
	ResponseTimeMs          int32
	OGTags                  []byte
	JSONLD                  []byte
	HeadingOutline          []byte
	ContentSHA256           string
}

// LinkFact holds the persisted internal link fields used for issue derivation.
type LinkFact struct {
	SourceURL string
	TargetURL string
}

// DerivedIssue holds one issue derived from persisted crawl facts.
type DerivedIssue struct {
	CrawlPageID pgtype.UUID
	URL         string
	Pillar      string
	Bucket      string
	IssueType   string
	Severity    string
	Message     string
	Details     string
}
