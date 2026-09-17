package competitorgaps

import (
	issueengine "github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// ratioDerivedSitewideIssueTypes were decided against the full crawl (e.g. OG
// coverage %). Carrying them into a hop slice lies about the cluster.
var ratioDerivedSitewideIssueTypes = map[string]struct{}{
	"weak_open_graph_coverage": {},
}

func scoreSlice(crawlID string, slice []graphPage, issues []pageIssue, psiResults []byte, scoringConfig shared.ScoringConfig) shared.ScoreBreakdownSnapshot {
	pages := make([]shared.CrawlPageSignal, 0, len(slice))
	sliceKeys := make(map[string]struct{}, len(slice))
	for _, page := range slice {
		pages = append(pages, shared.CrawlPageSignal{
			URL:         page.URL,
			StatusCode:  page.StatusCode,
			ContentType: page.ContentType,
			Soft404:     page.Soft404,
			FetchError:  page.FetchError,
		})
		sliceKeys[normalizeGraphURL(page.URL)] = struct{}{}
	}

	return issueengine.BuildScoreBreakdownWithConfig(
		crawlID,
		pages,
		sliceIssueSignals(issues, sliceKeys),
		scoringConfig,
		issueengine.ParseStoredGooglePSI(psiResults),
	)
}

func sliceIssueSignals(issues []pageIssue, sliceKeys map[string]struct{}) []shared.CrawlIssueSignal {
	signals := make([]shared.CrawlIssueSignal, 0, len(issues))
	for _, issue := range issues {
		if _, drop := ratioDerivedSitewideIssueTypes[issue.IssueType]; drop {
			continue
		}
		if shared.IsSitewideIssue(issue.IssueType) || shared.IsGooglePSIOriginIssue(issue.IssueType) {
			signals = append(signals, issue.toSignal())
			continue
		}
		if _, ok := sliceKeys[normalizeGraphURL(issue.URL)]; !ok {
			continue
		}
		signals = append(signals, issue.toSignal())
	}
	return signals
}

func (issue pageIssue) toSignal() shared.CrawlIssueSignal {
	return shared.CrawlIssueSignal{
		URL:       issue.URL,
		Pillar:    issue.Pillar,
		Bucket:    issue.Bucket,
		IssueType: issue.IssueType,
		Severity:  issue.Severity,
	}
}

func slicePageRows(pages []graphPage, hops map[string]int) []SlicePage {
	rows := make([]SlicePage, 0, len(pages))
	for _, page := range pages {
		rows = append(rows, SlicePage{
			URL: page.URL,
			Hop: hops[normalizeGraphURL(page.URL)],
		})
	}
	return rows
}

// SliceURLSet returns normalized URLs for one hop-matched side ("you" or "them").
func (report Report) SliceURLSet(side string) map[string]struct{} {
	pages := report.TheirSlice
	if side == "you" {
		pages = report.YourSlice
	}
	keys := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		keys[normalizeGraphURL(page.URL)] = struct{}{}
	}
	return keys
}

// NormalizeIssueURL is the same key used when hop-slicing pages.
func NormalizeIssueURL(raw string) string {
	return normalizeGraphURL(raw)
}
