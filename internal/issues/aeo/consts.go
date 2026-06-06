package aeo

const (
	PillarID     = "aeo"
	PillarLabel  = "AEO"
	PillarWeight = 0.20

	authorSignalMinimumWordCount        = 300
	strongCitationWordCountThreshold    = 600
	longArticleNoCitationWordCountThreshold = 800
	longArticleWeakCitationWordCountThreshold = 1200
	externalCitationMinimumCount        = 1
	weakExternalCitationMaximumCount    = 1
	weakOpenGraphCoverageThreshold      = 0.5
	faqLikeQuestionHeadingThreshold     = 2
	faqLikeQuestionMarkThreshold        = 3
)

var BucketWeights = map[string]float64{
	"experience":        0.15,
	"expertise":         0.20,
	"authoritativeness": 0.20,
	"trust":             0.20,
	"answerability":     0.25,
}

var IssuePenaltyByType = map[string]float64{
	"missing_author_signal":                 12,
	"weak_author_signal":                    6,
	"article_missing_publisher_identity":    12,
	"author_signal_not_supported_by_schema": 10,
	"missing_external_citations":            8,
	"weak_external_citations":               5,
	"long_article_has_no_citations":         14,
	"long_article_has_weak_citations":       10,
	"missing_org_identity_schema":           14,
	"missing_https":                         12,
	"missing_og_tags":                       4,
	"weak_open_graph_coverage":              7,
	"missing_website_schema":                12,
	"homepage_missing_org_contact_trust_signals": 14,
	"schema_missing_core_fields":            10,
	"missing_structured_data":               14,
	"generic_structured_data_only":          8,
	"article_missing_article_schema":        10,
	"faq_like_page_missing_faq_schema":      10,
	"missing_about_page":                    7,
	"missing_contact_page":                  7,
	"missing_policy_page":                   5,
}
