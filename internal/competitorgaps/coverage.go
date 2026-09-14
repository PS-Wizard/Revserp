package competitorgaps

import (
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const internalLinkingBucket = "internal_linking"

var issueRowsSpec = []struct {
	ID    string
	Label string
}{
	{ID: "serp_metadata", Label: "SERP metadata"},
	{ID: "content_structure", Label: "Content structure"},
	{ID: "content_quality", Label: "Content quality"},
	{ID: "indexability", Label: "Indexability"},
	{ID: "technical_seo", Label: "Technical SEO"},
	{ID: "media_optimization", Label: "Media optimization"},
	{ID: "aeo", Label: "AEO"},
}

var presenceRowsSpec = []struct {
	ID          string
	Label       string
	MissingType string
}{
	{ID: "about", Label: "About", MissingType: "missing_about_page"},
	{ID: "contact", Label: "Contact", MissingType: "missing_contact_page"},
	{ID: "policy", Label: "Policy", MissingType: "missing_policy_page"},
	{ID: "llms_txt", Label: "llms.txt", MissingType: "missing_llms_txt"},
	{ID: "organization_schema", Label: "Organization schema", MissingType: "missing_org_identity_schema"},
	{ID: "website_schema", Label: "Website schema", MissingType: "missing_website_schema"},
	{ID: "homepage_trust", Label: "Homepage trust", MissingType: "homepage_missing_org_contact_trust_signals"},
}

func issueCoverageRows(youSlice, themSlice []graphPage, youIssues, themIssues []pageIssue) []IssueRow {
	rows := make([]IssueRow, 0, len(issueRowsSpec))
	for _, spec := range issueRowsSpec {
		rows = append(rows, IssueRow{
			ID:    spec.ID,
			Label: spec.Label,
			You:   bucketCoveragePercent(youSlice, youIssues, spec.ID),
			Them:  bucketCoveragePercent(themSlice, themIssues, spec.ID),
		})
	}
	return rows
}

func bucketCoveragePercent(slice []graphPage, issues []pageIssue, bucketID string) int {
	if len(slice) == 0 {
		return 0
	}
	affectedKeys := make(map[string]struct{})
	for _, issue := range issues {
		if !issueCountsForBucket(issue, bucketID) {
			continue
		}
		affectedKeys[normalizeGraphURL(issue.URL)] = struct{}{}
	}
	affected := 0
	for _, page := range slice {
		if _, ok := affectedKeys[normalizeGraphURL(page.URL)]; ok {
			affected++
		}
	}
	return coveragePercent(affected, len(slice))
}

func issueCountsForBucket(issue pageIssue, bucketID string) bool {
	if !countsTowardSpread(issue) {
		return false
	}
	if bucketID == "aeo" {
		return issue.Pillar == "aeo"
	}
	return issue.Pillar == "seo" && issue.Bucket == bucketID
}

func countsTowardSpread(issue pageIssue) bool {
	if issue.Bucket == internalLinkingBucket {
		return false
	}
	return shared.IsPageAddressableIssue(issue.IssueType)
}

func spreadRows(youSlice, themSlice []graphPage, youIssues, themIssues []pageIssue) []SpreadRow {
	youPages, youPillars := spreadPagesByType(youSlice, youIssues)
	themPages, themPillars := spreadPagesByType(themSlice, themIssues)

	types := make(map[string]string, len(youPillars)+len(themPillars))
	for issueType, pillar := range youPillars {
		types[issueType] = pillar
	}
	for issueType, pillar := range themPillars {
		if _, ok := types[issueType]; !ok {
			types[issueType] = pillar
		}
	}

	rows := make([]SpreadRow, 0, len(types))
	for issueType, pillar := range types {
		you := spreadPercent(len(youPages[issueType]), len(youSlice))
		them := spreadPercent(len(themPages[issueType]), len(themSlice))
		if you == 0 && them == 0 {
			continue
		}
		rows = append(rows, SpreadRow{
			ID:     issueType,
			Label:  shared.HumanizeIdentifier(issueType),
			Pillar: pillar,
			You:    you,
			Them:   them,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		iGap := math.Abs(rows[i].You - rows[i].Them)
		jGap := math.Abs(rows[j].You - rows[j].Them)
		if iGap != jGap {
			return iGap > jGap
		}
		return rows[i].Label < rows[j].Label
	})
	return rows
}

func spreadPagesByType(slice []graphPage, issues []pageIssue) (map[string]map[string]struct{}, map[string]string) {
	sliceKeys := make(map[string]struct{}, len(slice))
	for _, page := range slice {
		sliceKeys[normalizeGraphURL(page.URL)] = struct{}{}
	}

	pagesByType := make(map[string]map[string]struct{})
	pillarByType := make(map[string]string)
	for _, issue := range issues {
		if !countsTowardSpread(issue) {
			continue
		}
		pageKey := normalizeGraphURL(issue.URL)
		if _, ok := sliceKeys[pageKey]; !ok {
			continue
		}
		pages := pagesByType[issue.IssueType]
		if pages == nil {
			pages = make(map[string]struct{})
			pagesByType[issue.IssueType] = pages
			pillarByType[issue.IssueType] = issue.Pillar
		}
		pages[pageKey] = struct{}{}
	}
	return pagesByType, pillarByType
}

func spreadPercent(affected, total int) float64 {
	if total <= 0 {
		return 0
	}
	return 100 * float64(affected) / float64(total)
}

func pageHealthSide(slice []graphPage, issues []pageIssue) PageHealthSide {
	buckets := make([]int, PageHealthBuckets)
	if len(slice) == 0 {
		return PageHealthSide{Buckets: buckets, TotalPages: 0}
	}

	countByPage := make(map[string]int, len(slice))
	for _, issue := range issues {
		if !countsTowardSpread(issue) {
			continue
		}
		countByPage[normalizeGraphURL(issue.URL)]++
	}
	for _, page := range slice {
		count := countByPage[normalizeGraphURL(page.URL)]
		if count >= PageHealthBuckets {
			count = PageHealthBuckets - 1
		}
		buckets[count]++
	}
	return PageHealthSide{Buckets: buckets, TotalPages: len(slice)}
}

func coveragePercent(affected, total int) int {
	if total <= 0 {
		return 0
	}
	percent := int(math.Round(100 * float64(affected) / float64(total)))
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func presenceRows(youIssues, themIssues []pageIssue, youHasLlmsTxt, themHasLlmsTxt pgtype.Bool) []PresenceRow {
	rows := make([]PresenceRow, 0, len(presenceRowsSpec))
	for _, spec := range presenceRowsSpec {
		rows = append(rows, PresenceRow{
			ID:    spec.ID,
			Label: spec.Label,
			You:   presenceValue(spec.ID, spec.MissingType, youIssues, youHasLlmsTxt),
			Them:  presenceValue(spec.ID, spec.MissingType, themIssues, themHasLlmsTxt),
		})
	}
	return rows
}

func presenceValue(id, missingType string, issues []pageIssue, hasLlmsTxt pgtype.Bool) bool {
	if id == "llms_txt" {
		if hasLlmsTxt.Valid {
			return hasLlmsTxt.Bool
		}
		return !hasIssueType(issues, missingType)
	}
	return !hasIssueType(issues, missingType)
}

func hasIssueType(issues []pageIssue, issueType string) bool {
	for _, issue := range issues {
		if issue.IssueType == issueType {
			return true
		}
	}
	return false
}

func contentSignals(slice []graphPage) ContentSignals {
	if len(slice) == 0 {
		return ContentSignals{}
	}
	wordCounts := make([]int, 0, len(slice))
	withH1 := 0
	withMeta := 0
	for _, page := range slice {
		wordCounts = append(wordCounts, page.WordCount)
		if strings.TrimSpace(page.H1) != "" {
			withH1++
		}
		if strings.TrimSpace(page.MetaDescription) != "" {
			withMeta++
		}
	}
	return ContentSignals{
		MedianWordCount: medianInt(wordCounts),
		PagesWithH1:     coveragePercent(withH1, len(slice)),
		PagesWithMeta:   coveragePercent(withMeta, len(slice)),
	}
}

func medianInt(values []int) *int {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	n := len(sorted)
	if n%2 == 1 {
		value := sorted[n/2]
		return &value
	}
	value := int(math.Round(float64(sorted[n/2-1]+sorted[n/2]) / 2))
	return &value
}
