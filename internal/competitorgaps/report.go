package competitorgaps

import "github.com/ps-wizard/revserp/internal/issues/shared"

// ReportVersion is stored with every persisted snapshot so we can rebuild
// without recrawling when the comparison rules change.
const ReportVersion = "v4"

// Report is the competitor gap snapshot. JSON keys are the API contract.
type Report struct {
	Version       string                        `json:"version"`
	Radius        int                           `json:"radius"`
	YourPages     int                           `json:"your_pages"`
	TheirPages    int                           `json:"their_pages"`
	YouBreakdown  shared.ScoreBreakdownSnapshot `json:"you_breakdown"`
	ThemBreakdown shared.ScoreBreakdownSnapshot `json:"them_breakdown"`
	YourSlice     []SlicePage                   `json:"your_slice"`
	TheirSlice    []SlicePage                   `json:"their_slice"`
	Issues        []IssueRow                    `json:"issues"`
	Spread        []SpreadRow                   `json:"spread"`
	PageHealth    PageHealthCompare             `json:"page_health"`
	Presence      []PresenceRow                 `json:"presence"`
	PSI           PSICompare                    `json:"psi"`
	Homepage      HomepageCompare               `json:"homepage"`
	Content       ContentCompare                `json:"content"`
}

// SlicePage is one hop-matched scoreable page used for slice scoring and
// issue drilldown. Hop is BFS distance from that site's homepage.
type SlicePage struct {
	URL string `json:"url"`
	Hop int    `json:"hop"`
}

type IssueRow struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	You   int    `json:"you"`
	Them  int    `json:"them"`
}

// SpreadRow is one issue type's prevalence on the hop-matched slice.
// You/Them are 0–100 percents of that side's slice pages.
type SpreadRow struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Pillar string  `json:"pillar"`
	You    float64 `json:"you"`
	Them   float64 `json:"them"`
}

// PageHealthBuckets is 0..19 issues plus a 20+ tail, matching crawl page-health.
const PageHealthBuckets = 21

type PageHealthCompare struct {
	You  PageHealthSide `json:"you"`
	Them PageHealthSide `json:"them"`
}

type PageHealthSide struct {
	Buckets    []int `json:"buckets"`
	TotalPages int   `json:"total_pages"`
}

type PresenceRow struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	You   bool   `json:"you"`
	Them  bool   `json:"them"`
}

type PSICompare struct {
	You  *PSIMetrics `json:"you"`
	Them *PSIMetrics `json:"them"`
}

type PSIMetrics struct {
	Performance *int     `json:"performance"`
	LCP         *float64 `json:"lcp"`
	FCP         *float64 `json:"fcp"`
	CLS         *float64 `json:"cls"`
	FID         *float64 `json:"fid"`
	SpeedIndex  *float64 `json:"speed_index"`
	TTI         *float64 `json:"tti"`
}

type HomepageCompare struct {
	You  HomepageSignals `json:"you"`
	Them HomepageSignals `json:"them"`
}

type HomepageSignals struct {
	URL       string `json:"url"`
	Title     bool   `json:"title"`
	Meta      bool   `json:"meta"`
	H1        bool   `json:"h1"`
	Canonical bool   `json:"canonical"`
	OG        bool   `json:"og"`
	JSONLD    bool   `json:"json_ld"`
	WordCount int    `json:"word_count"`
}

type ContentCompare struct {
	You  ContentSignals `json:"you"`
	Them ContentSignals `json:"them"`
}

type ContentSignals struct {
	MedianWordCount *int `json:"median_word_count"`
	PagesWithH1     int  `json:"pages_with_h1"`
	PagesWithMeta   int  `json:"pages_with_meta"`
}
