package issues

import (
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// PageHealthPageSignal holds the persisted page fields used for page-health scoring.
type PageHealthPageSignal struct {
	CrawlPageID pgtype.UUID
	StatusCode  int32
	ContentType string
	Soft404     bool
	FetchError  string
}

// PageHealthIssueSignal holds one issue row used for page-health scoring.
type PageHealthIssueSignal struct {
	CrawlPageID pgtype.UUID
	Pillar      string
	Bucket      string
	IssueType   string
	Severity    string
}

// PageHealthBucketScore explains one scored bucket inside a page-health pillar.
type PageHealthBucketScore struct {
	ID    string `json:"id"`
	Score int32  `json:"score"`
}

// PageHealthPillarScore explains one scored pillar of a page-health breakdown.
type PageHealthPillarScore struct {
	ID      string                  `json:"id"`
	Score   int32                   `json:"score"`
	Buckets []PageHealthBucketScore `json:"buckets"`
}

// PageHealthScore holds one page's overall health score plus its detailed breakdown.
type PageHealthScore struct {
	CrawlPageID pgtype.UUID             `json:"crawl_page_id"`
	HealthScore int16                   `json:"health_score"`
	Pillars     []PageHealthPillarScore `json:"pillars"`
}

// pageHealthPillarOrder is the stable pillar order for page-health breakdowns.
var pageHealthPillarOrder = [...]string{seo.PillarID, aeo.PillarID, pagespeed.PillarID}

// pageHealthGroupKey keys deduplicated penalties by pillar/bucket within one page.
type pageHealthGroupKey string

// pageHealthExcludedBucketID is the origin-level PageSpeed bucket: it cannot apply
// to a single page, so it is omitted from every page-health breakdown and pillar math.
const pageHealthExcludedBucketID = "psi_cwv"

// CalculatePageHealthScores computes per-page health scores with deterministic
// pillar/bucket breakdowns from the same configurable scoring math as crawl scoring.
// Fetch-error pages are unscored and produce no result; hard errors and soft 404s
// produce an all-zero breakdown; other non-HTML pages are skipped entirely.
func CalculatePageHealthScores(pages []PageHealthPageSignal, issues []PageHealthIssueSignal, config shared.ScoringConfig) []PageHealthScore {
	// Deduplicate: pageID -> pillar/bucket -> issueType -> max penalty.
	deduped := make(map[string]map[pageHealthGroupKey]map[string]float64)
	for _, iss := range issues {
		if !iss.CrawlPageID.Valid || !shared.IsPageAddressableIssue(iss.IssueType) {
			continue
		}
		pageIDStr := uuidKey(iss.CrawlPageID)
		gk := pageHealthGroupKey(iss.Pillar + "\x00" + iss.Bucket)
		penalty := issuePenaltyForPageHealth(iss, config)
		pageGroups := deduped[pageIDStr]
		if pageGroups == nil {
			pageGroups = make(map[pageHealthGroupKey]map[string]float64)
			deduped[pageIDStr] = pageGroups
		}
		group := pageGroups[gk]
		if group == nil {
			group = make(map[string]float64)
			pageGroups[gk] = group
		}
		key := strings.TrimSpace(iss.IssueType)
		if existing, ok := group[key]; !ok || penalty > existing {
			group[key] = penalty
		}
	}

	out := make([]PageHealthScore, 0, len(pages))
	for _, page := range pages {
		if page.StatusCode >= 400 || page.Soft404 {
			out = append(out, PageHealthScore{CrawlPageID: page.CrawlPageID, HealthScore: 0, Pillars: zeroPageHealthPillars(config)})
			continue
		}
		if strings.TrimSpace(page.FetchError) != "" {
			continue
		}
		if !shared.IsScoreableContentType(page.ContentType) {
			continue
		}
		pageGroups := deduped[uuidKey(page.CrawlPageID)]
		pillars, healthScore := buildPageHealthPillars(pageGroups, config)
		out = append(out, PageHealthScore{CrawlPageID: page.CrawlPageID, HealthScore: int16(healthScore), Pillars: pillars})
	}
	return out
}

// buildPageHealthPillars builds the scored pillar/bucket breakdown and the overall score.
func buildPageHealthPillars(pageGroups map[pageHealthGroupKey]map[string]float64, config shared.ScoringConfig) ([]PageHealthPillarScore, int32) {
	pillars := make([]PageHealthPillarScore, 0, len(pageHealthPillarOrder))
	pillarScoreByID := make(map[string]int32, len(pageHealthPillarOrder))
	for _, pillarID := range pageHealthPillarOrder {
		pillarConfig, ok := config.Pillars[pillarID]
		if !ok {
			continue
		}
		buckets := make([]PageHealthBucketScore, 0, len(pillarConfig.BucketWeights))
		weightedBucketScoreSum := 0.0
		bucketWeightSum := 0.0
		for _, bucketID := range shared.SortedBucketIDs(pillarConfig.BucketWeights) {
			if pillarID == pagespeed.PillarID && bucketID == pageHealthExcludedBucketID {
				continue
			}
			bucketWeight := pillarConfig.BucketWeights[bucketID]
			bucketScore := pageHealthBucketScore(pageGroups, pillarID, bucketID, config)
			buckets = append(buckets, PageHealthBucketScore{ID: bucketID, Score: bucketScore})
			weightedBucketScoreSum += float64(bucketScore) * bucketWeight
			bucketWeightSum += bucketWeight
		}
		// Normalize remaining bucket weights so they always sum to 1; the origin-level
		// psi_cwv bucket is excluded above, so PageSpeed must not inherit a fake 100
		// for a bucket that cannot apply to a single page.
		pillarScore := int32(100)
		if bucketWeightSum > 0 {
			pillarScore = shared.ClampScore(weightedBucketScoreSum/bucketWeightSum, 0)
		}
		pillarScoreByID[pillarID] = pillarScore
		pillars = append(pillars, PageHealthPillarScore{ID: pillarID, Score: pillarScore, Buckets: buckets})
	}
	overall := calculateOverallScore(pillarScoreByID[seo.PillarID], pillarScoreByID[aeo.PillarID], pillarScoreByID[pagespeed.PillarID], config)
	return pillars, overall
}

// pageHealthBucketScore scores one bucket: soft-summed penalties, no crawl coverage.
func pageHealthBucketScore(pageGroups map[pageHealthGroupKey]map[string]float64, pillarID string, bucketID string, config shared.ScoringConfig) int32 {
	penaltyByType := pageGroups[pageHealthGroupKey(pillarID+"\x00"+bucketID)]
	if len(penaltyByType) == 0 {
		return 100
	}
	penalties := make([]float64, 0, len(penaltyByType))
	for _, penalty := range penaltyByType {
		penalties = append(penalties, penalty)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(penalties)))
	breakdowns := make([]shared.IssueTypeScoreBreakdown, len(penalties))
	for index, penalty := range penalties {
		breakdowns[index] = shared.IssueTypeScoreBreakdown{FinalPenalty: penalty}
	}
	return shared.ClampScore(100-shared.SoftSumPenalties(breakdowns, config), 0)
}

// zeroPageHealthPillars builds the full breakdown shape with every score at zero.
func zeroPageHealthPillars(config shared.ScoringConfig) []PageHealthPillarScore {
	pillars := make([]PageHealthPillarScore, 0, len(pageHealthPillarOrder))
	for _, pillarID := range pageHealthPillarOrder {
		pillarConfig, ok := config.Pillars[pillarID]
		if !ok {
			continue
		}
		buckets := make([]PageHealthBucketScore, 0, len(pillarConfig.BucketWeights))
		for _, bucketID := range shared.SortedBucketIDs(pillarConfig.BucketWeights) {
			if pillarID == pagespeed.PillarID && bucketID == pageHealthExcludedBucketID {
				continue
			}
			buckets = append(buckets, PageHealthBucketScore{ID: bucketID, Score: 0})
		}
		pillars = append(pillars, PageHealthPillarScore{ID: pillarID, Score: 0, Buckets: buckets})
	}
	return pillars
}

func issuePenaltyForPageHealth(iss PageHealthIssueSignal, config shared.ScoringConfig) float64 {
	var penaltyByType map[string]float64
	if pillarConfig, ok := config.Pillars[iss.Pillar]; ok {
		penaltyByType = pillarConfig.IssuePenaltyByType
	}
	base := shared.IssueBasePenalty(iss.IssueType, penaltyByType)
	multiplier := shared.SeverityMultiplierWithConfig(iss.Severity, config)
	return base * multiplier
}

func uuidKey(id pgtype.UUID) string {
	// pgtype.UUID Bytes is [16]byte
	return string(id.Bytes[:])
}
