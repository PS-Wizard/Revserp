package issues

import (
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

type PageHealthPageSignal struct {
	CrawlPageID pgtype.UUID
	StatusCode  int32
	ContentType string
	Soft404     bool
	FetchError  string
}

type PageHealthIssueSignal struct {
	CrawlPageID pgtype.UUID
	Pillar      string
	Bucket      string
	IssueType   string
	Severity    string
}

type PageHealthScore struct {
	CrawlPageID pgtype.UUID
	HealthScore int16
}

func CalculatePageHealthScores(pages []PageHealthPageSignal, issues []PageHealthIssueSignal, config shared.ScoringConfig) []PageHealthScore {
	// Deduplicate: pageID -> groupKey -> issueType -> maxPenalty
	type groupKey string
	deduped := make(map[string]map[groupKey]map[string]float64)

	for _, iss := range issues {
		if !iss.CrawlPageID.Valid {
			continue
		}
		if !shared.IsPageAddressableIssue(iss.IssueType) {
			continue
		}
		penalty := issuePenaltyForPageHealth(iss, config)
		pageIDStr := uuidKey(iss.CrawlPageID)
		if _, ok := deduped[pageIDStr]; !ok {
			deduped[pageIDStr] = make(map[groupKey]map[string]float64)
		}
		gk := groupKey(iss.Pillar + "\x00" + iss.Bucket)
		if _, ok := deduped[pageIDStr][gk]; !ok {
			deduped[pageIDStr][gk] = make(map[string]float64)
		}
		key := strings.TrimSpace(iss.IssueType)
		if existing, ok := deduped[pageIDStr][gk][key]; !ok || penalty > existing {
			deduped[pageIDStr][gk][key] = penalty
		}
	}

	var out []PageHealthScore
	for _, page := range pages {
		if isBrokenPage(page) {
			out = append(out, PageHealthScore{CrawlPageID: page.CrawlPageID, HealthScore: 0})
			continue
		}
		if !shared.IsScoreableContentType(page.ContentType) {
			continue
		}
		pageIDStr := uuidKey(page.CrawlPageID)
		groups := deduped[pageIDStr]
		if len(groups) == 0 {
			out = append(out, PageHealthScore{CrawlPageID: page.CrawlPageID, HealthScore: 100})
			continue
		}
		totalPenalty := 0.0
		groupKeys := make([]groupKey, 0, len(groups))
		for k := range groups {
			groupKeys = append(groupKeys, k)
		}
		sort.Slice(groupKeys, func(i, j int) bool { return groupKeys[i] < groupKeys[j] })
		for _, gk := range groupKeys {
			issueMap := groups[gk]
			penalties := make([]float64, 0, len(issueMap))
			for _, p := range issueMap {
				penalties = append(penalties, p)
			}
			sort.Sort(sort.Reverse(sort.Float64Slice(penalties)))
			// Use existing soft-sum helper via IssueTypeScoreBreakdown.
			breakdowns := make([]shared.IssueTypeScoreBreakdown, len(penalties))
			for i, p := range penalties {
				breakdowns[i] = shared.IssueTypeScoreBreakdown{FinalPenalty: p}
			}
			totalPenalty += shared.SoftSumPenalties(breakdowns, config)
		}
		score := shared.ClampScore(100-totalPenalty, 0)
		out = append(out, PageHealthScore{CrawlPageID: page.CrawlPageID, HealthScore: int16(score)})
	}
	return out
}

func isBrokenPage(page PageHealthPageSignal) bool {
	if page.StatusCode >= 400 {
		return true
	}
	if page.Soft404 {
		return true
	}
	if strings.TrimSpace(page.FetchError) != "" {
		return true
	}
	return false
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
