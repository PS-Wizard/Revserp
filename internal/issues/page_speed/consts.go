package pagespeed

const (
	PillarID                              = "pagespeed"
	PillarLabel                           = "PageSpeed"
	PillarWeight                          = 0.15
	slowResponseTimeMillisecondsThreshold = 1000
	moderatePageSizeBytesThreshold        = 1 * 1024 * 1024
	largePageSizeBytesThreshold           = 3 * 1024 * 1024
)

var BucketWeights = map[string]float64{
	"server_responsiveness": 0.40,
	"page_weight":           0.30,
	"psi_cwv":               0.30,
}

var IssuePenaltyByType = map[string]float64{
	"slow_response_time":            12,
	"moderate_page_size":            5,
	"large_page_size":               10,
	"google_psi_mobile_performance": 18,
	"google_psi_lcp":                14,
	"google_psi_cls":                12,
}
