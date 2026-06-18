package shared

import "github.com/jackc/pgx/v5/pgtype"

// PageFact holds the persisted page fields used for issue derivation.
type PageFact struct {
	ID                      pgtype.UUID
	URL                     string
	ContentType             string
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
	SourceURL    string
	TargetURL    string
	TargetStatus int32
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

// ScoreBreakdownSnapshot holds one persisted crawl scoring explanation snapshot.
type ScoreBreakdownSnapshot struct {
	CrawlID          string                 `json:"crawl_id"`
	ScoringVersion   string                 `json:"scoring_version"`
	CoverageScale    float64                `json:"coverage_scale"`
	TotalScoredPages int32                  `json:"total_scored_pages"`
	OverallScore     int32                  `json:"overall_score"`
	Pillars          []PillarScoreBreakdown `json:"pillars"`
}

// CrawlScores converts one snapshot back into the persisted top-level crawl scores.
func (snapshot ScoreBreakdownSnapshot) CrawlScores() CrawlScores {
	crawlScores := CrawlScores{OverallScore: snapshot.OverallScore}
	for _, pillar := range snapshot.Pillars {
		switch pillar.ID {
		case "seo":
			crawlScores.SEOScore = pillar.Score
		case "aeo":
			crawlScores.AEOScore = pillar.Score
		case "pagespeed":
			crawlScores.PageSpeedScore = pillar.Score
		}
	}
	return crawlScores
}

// PillarScoreBreakdown explains one pillar's score and weighted buckets.
type PillarScoreBreakdown struct {
	ID                   string                 `json:"id"`
	Label                string                 `json:"label"`
	Score                int32                  `json:"score"`
	Weight               float64                `json:"weight"`
	WeightedContribution float64                `json:"weighted_contribution"`
	TotalPenalty         float64                `json:"total_penalty"`
	BucketCount          int32                  `json:"bucket_count"`
	IssueTypeCount       int32                  `json:"issue_type_count"`
	IssueRowCount        int32                  `json:"issue_row_count"`
	AffectedURLCount     int32                  `json:"affected_url_count"`
	Buckets              []BucketScoreBreakdown `json:"buckets"`
}

// BucketScoreBreakdown explains one weighted bucket inside a pillar.
type BucketScoreBreakdown struct {
	ID                   string                    `json:"id"`
	Label                string                    `json:"label"`
	Score                int32                     `json:"score"`
	Weight               float64                   `json:"weight"`
	WeightedContribution float64                   `json:"weighted_contribution"`
	TotalPenalty         float64                   `json:"total_penalty"`
	IssueTypeCount       int32                     `json:"issue_type_count"`
	IssueRowCount        int32                     `json:"issue_row_count"`
	AffectedURLCount     int32                     `json:"affected_url_count"`
	Issues               []IssueTypeScoreBreakdown `json:"issues"`
}

// IssueTypeScoreBreakdown explains one grouped issue type inside a bucket.
type IssueTypeScoreBreakdown struct {
	ID                 string  `json:"id"`
	Label              string  `json:"label"`
	Severity           string  `json:"severity"`
	BasePenalty        float64 `json:"base_penalty"`
	SeverityMultiplier float64 `json:"severity_multiplier"`
	Coverage           float64 `json:"coverage"`
	FinalPenalty       float64 `json:"final_penalty"`
	IssueRowCount      int32   `json:"issue_row_count"`
	AffectedURLCount   int32   `json:"affected_url_count"`
	Message            string  `json:"message"`
	DetailsPreview     string  `json:"details_preview"`
}

// GooglePSIScoreInput carries PSI-derived scores that override issue-based bucket scoring.
type GooglePSIScoreInput struct {
	MobilePerformanceScore *int
}
