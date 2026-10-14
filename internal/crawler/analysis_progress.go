package crawler

const (
	AnalysisStageCheckingLinks  = "checking_links"
	AnalysisStageDerivingIssues = "deriving_issues"
	AnalysisStageScoring        = "scoring"
)

// CrawlAnalysisProgress is the persisted analysis_progress JSON: which
// post-fetch stage a running crawl is in, plus actual completed SQL affected
// rows. Counts only, never percent/ETA/totals.
type CrawlAnalysisProgress struct {
	Stage         string `json:"stage"`
	LinksResolved int64  `json:"links_resolved,omitempty"`
}
