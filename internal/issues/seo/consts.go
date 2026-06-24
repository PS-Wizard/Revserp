package seo

const (
	PillarID     = "seo"
	PillarLabel  = "SEO"
	PillarWeight = 0.65

	thinContentWordCountThreshold          = 150
	nearEmptyVisibleContentWordThreshold   = 25
	longPageWordCountThreshold             = 300
	shortTitleCharacterThreshold           = 30
	longTitleCharacterThreshold            = 60
	shortMetaDescriptionCharacterThreshold = 120
	longMetaDescriptionCharacterThreshold  = 160
	titleH1MismatchSimilarityThreshold     = 0.32
	lowInternalLinksOutThreshold           = 2
	lowInternalLinksInThreshold            = 2
	veryDeepPageDepthThreshold             = 4
	tooManyImagesMinimumImageCount         = 10
	tooManyImagesWordsPerImageThreshold    = 50
)

var BucketWeights = map[string]float64{
	"serp_metadata":      0.20,
	"content_structure":  0.16,
	"content_quality":    0.20,
	"indexability":       0.20,
	"technical_seo":      0.08,
	"media_optimization": 0.06,
	"internal_linking":   0.10,
}

var IssuePenaltyByType = map[string]float64{
	"missing_title":                          12,
	"title_too_long":                         5,
	"title_too_short":                        5,
	"duplicate_title":                        10,
	"missing_meta_description":               10,
	"meta_description_too_long":              4,
	"meta_description_too_short":             4,
	"duplicate_meta_description":             8,
	"missing_h1":                             10,
	"multiple_h1":                            7,
	"title_h1_mismatch":                      6,
	"missing_h2_on_long_page":                6,
	"skipped_heading_levels":                 6,
	"thin_content":                           12,
	"near_empty_visible_content":             12,
	"exact_duplicate_content":                12,
	"near_duplicate_content":                 8,
	"missing_canonical":                      8,
	"canonical_differs":                      5,
	"malformed_canonical":                    10,
	"canonical_points_to_non_indexable_page": 12,
	"noindex_page":                           14,
	"nofollow_page":                          6,
	"missing_viewport":                       8,
	"missing_lang":                           4,
	"client_error_status":                    12,
	"server_error_status":                    14,
	"images_missing_alt":                     5,
	"images_missing_dimensions":              4,
	"too_many_images_on_page":                4,
	"orphan_like_page":                       10,
	"low_internal_links_in":                  8,
	"very_deep_page":                         6,
	"low_internal_links_out":                 5,
	"no_internal_links_out":                  8,
	"internal_links_to_broken_pages":         12,
	"internal_links_to_redirects":            6,
}
