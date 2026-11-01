package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	guuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/keywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

// getCompetitorKeywordsName reads one competitor's stored AI keyword
// estimates plus per-keyword coverage on the competitor's own latest crawl.
const getCompetitorKeywordsName = "get_competitor_keywords"

const getCompetitorKeywordsMaxKeywords = 50

const getCompetitorKeywordsSchema = `{
  "type": "object",
  "properties": {
    "competitor": {"type": "string", "description": "Competitor roster ID or case-insensitive name substring. Omit when the project has exactly one competitor; with several competitors it is required to disambiguate."},
    "type": {"type": "string", "enum": ["brand", "non_brand", "all"], "default": "non_brand", "description": "Which keyword kind to return. Defaults to non_brand."}
  },
  "additionalProperties": false
}`

type getCompetitorKeywordsArgs struct {
	Competitor string
	Kind       string
}

func getCompetitorKeywordsTool() Tool {
	return Tool{
		Def: Def{
			Name:        getCompetitorKeywordsName,
			Label:       "Get competitor keywords",
			Description: "Read one competitor's keywords with per-keyword coverage on the competitor's own latest completed crawl: each keyword gets a state (likely_targeted means exactly one competitor page matches in title or h1, no_landing_page means no page matches and it is a gap, cannibalized means two or more pages compete) plus the matching page URLs, and the stored AI metric estimates when set. Defaults to non_brand keywords; pass brand or all for the other kinds. At most 50 keywords are returned. When the competitor has no completed crawl yet, keywords are listed with state uncrawled and no coverage. Project scoped only: location turns are denied.",
			Schema:      json.RawMessage(getCompetitorKeywordsSchema),
		},
		Execute: executeGetCompetitorKeywords,
	}
}

type getCompetitorKeywordsExecutor struct {
	store    competitorRosterStore
	analyses competitorAnalysesLister
	cover    func(pages []keywords.Page, targetKeywords []string, primaryLocation string) []keywords.Seed
}

func (e *getCompetitorKeywordsExecutor) coverFunc() func([]keywords.Page, []string, string) []keywords.Seed {
	if e.cover != nil {
		return e.cover
	}
	return keywords.Cover
}

type getCompetitorKeywordsRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	SeedURL     string `json:"seed_url"`
	CrawlStatus string `json:"crawl_status"`
}

type getCompetitorKeywordEntry struct {
	Keyword      string           `json:"keyword"`
	Kind         string           `json:"kind"`
	State        string           `json:"state"`
	MatchCount   int              `json:"match_count"`
	Matches      []keywords.Match `json:"matches"`
	Rank         *int32           `json:"rank,omitempty"`
	Traffic      *int64           `json:"traffic,omitempty"`
	Volume       *int64           `json:"volume,omitempty"`
	KD           *float64         `json:"kd,omitempty"`
	CPC          *float64         `json:"cpc,omitempty"`
	ResultsCount *int64           `json:"results_count,omitempty"`
}

type getCompetitorKeywordsResponse struct {
	Competitor        getCompetitorKeywordsRef    `json:"competitor"`
	Type              string                      `json:"type"`
	CrawlID           *string                     `json:"crawl_id"`
	CoverageAvailable bool                        `json:"coverage_available"`
	Note              string                      `json:"note,omitempty"`
	Counts            keywordCoverageCounts       `json:"counts"`
	TotalKeywords     int                         `json:"total_keywords"`
	Truncated         bool                        `json:"truncated"`
	Keywords          []getCompetitorKeywordEntry `json:"keywords"`
}

func executeGetCompetitorKeywords(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.LocationID.Valid {
		return Result{Content: getCompetitorKeywordsName + " error: location turns cannot read project competitor keywords"}, nil
	}
	if s.Queries == nil {
		return Result{}, errors.New("get_competitor_keywords: scope has no queries")
	}
	exec := getCompetitorKeywordsExecutor{store: s.Queries, analyses: liveCompetitorAnalyses{queries: s.Queries}}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

// run executes one get_competitor_keywords call. Membership is rechecked
// through the project join even though the scope already carries the
// project, so a stale or forged scope cannot read another project's roster.
func (e *getCompetitorKeywordsExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseGetCompetitorKeywordsArgs(raw)
	if err != nil {
		return Result{Content: getCompetitorKeywordsName + " error: " + err.Error()}, nil
	}
	if _, err := e.store.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{Content: getCompetitorKeywordsName + " error: project not found or access denied"}, nil
		}
		return Result{}, fmt.Errorf("%s: check project access: %w", getCompetitorKeywordsName, err)
	}
	roster, err := e.store.ListProjectCompetitorsForUser(ctx, sqlc.ListProjectCompetitorsForUserParams{UserID: userID, ProjectID: projectID})
	if err != nil {
		return Result{}, fmt.Errorf("%s: list competitors: %w", getCompetitorKeywordsName, err)
	}
	if len(roster) == 0 {
		return Result{
			Content: getCompetitorKeywordsName + ": no competitors saved yet for this project. Use add_competitor to save one first.",
			Summary: "no competitors saved",
		}, nil
	}
	competitor, disambiguation, err := resolveCompetitorArg(roster, args.Competitor)
	if err != nil {
		return Result{}, err
	}
	if disambiguation != "" {
		return Result{Content: disambiguation, Summary: "competitor disambiguation needed"}, nil
	}
	analyses, err := e.analyses.ListCompetitorAnalyses(ctx, competitor.ID)
	if err != nil {
		return Result{}, fmt.Errorf("%s: list competitor keywords: %w", getCompetitorKeywordsName, err)
	}
	filtered := filterCompetitorAnalyses(analyses, args.Kind)
	truncated := len(filtered) > getCompetitorKeywordsMaxKeywords
	filtered = filtered[:min(len(filtered), getCompetitorKeywordsMaxKeywords)]

	ref, crawlID, coverByKeyword, coverageAvailable := e.coverCompetitorKeywords(ctx, projectID, competitor, filtered)
	entries, counts := shapeCompetitorKeywordEntries(filtered, coverByKeyword, coverageAvailable)

	response := getCompetitorKeywordsResponse{
		Competitor:        ref,
		Type:              args.Kind,
		CrawlID:           crawlID,
		CoverageAvailable: coverageAvailable,
		Counts:            counts,
		TotalKeywords:     len(filtered),
		Truncated:         truncated,
		Keywords:          entries,
	}
	if !coverageAvailable {
		response.Note = "competitor has no completed crawl yet, so keywords are listed without coverage"
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", getCompetitorKeywordsName, err)
	}
	summary := fmt.Sprintf("competitor %s: %d %s keywords", competitor.Name, len(filtered), args.Kind)
	if coverageAvailable {
		summary = fmt.Sprintf("competitor %s: %d targeted, %d gaps, %d cannibalized of %d %s keywords",
			competitor.Name, counts.LikelyTargeted, counts.NoLandingPage, counts.Cannibalized, len(filtered), args.Kind)
	} else if truncated {
		summary += " (truncated, not crawled)"
	} else {
		summary += " (not crawled yet)"
	}
	return Result{Content: string(content), Summary: summary}, nil
}

// coverCompetitorKeywords measures the competitor's own keywords against the
// competitor's own latest child crawl pages, the same them-on-them side as
// the competitor gap report. Without a completed competitor crawl there is
// no coverage to report.
func (e *getCompetitorKeywordsExecutor) coverCompetitorKeywords(ctx context.Context, projectID pgtype.UUID, competitor sqlc.ProjectCompetitor, analyses []keywordanalysis.Analysis) (getCompetitorKeywordsRef, *string, map[string]keywords.Seed, bool) {
	ref := getCompetitorKeywordsRef{ID: competitor.ID.String(), Name: competitor.Name, SeedURL: competitor.SeedUrl, CrawlStatus: "not_crawled"}
	empty := map[string]keywords.Seed{}
	parentCrawlID, err := e.store.GetLatestCompletedCrawlForProject(ctx, projectID)
	if err != nil {
		return ref, nil, empty, false
	}
	child, err := e.store.GetCompetitorChildCrawlForParent(ctx, sqlc.GetCompetitorChildCrawlForParentParams{ParentCrawlID: parentCrawlID, CompetitorID: competitor.ID})
	if err != nil {
		return ref, nil, empty, false
	}
	ref.CrawlStatus = child.Status
	if child.Status != "completed" {
		return ref, nil, empty, false
	}
	pageRows, err := e.store.ListKeywordCoveragePagesForCrawl(ctx, child.ID)
	if err != nil {
		return ref, nil, empty, false
	}
	targets := make([]string, 0, len(analyses))
	for _, analysis := range analyses {
		targets = append(targets, analysis.Keyword)
	}
	seeds := e.coverFunc()(keywordCoveragePagesFromRows(pageRows), targets, "")
	byKeyword := make(map[string]keywords.Seed, len(seeds))
	for _, seed := range seeds {
		key := strings.ToLower(seed.Keyword)
		if _, dup := byKeyword[key]; !dup {
			byKeyword[key] = seed
		}
	}
	crawlID := child.ID.String()
	return ref, &crawlID, byKeyword, true
}

func shapeCompetitorKeywordEntries(analyses []keywordanalysis.Analysis, coverByKeyword map[string]keywords.Seed, coverageAvailable bool) ([]getCompetitorKeywordEntry, keywordCoverageCounts) {
	entries := make([]getCompetitorKeywordEntry, 0, len(analyses))
	var counts keywordCoverageCounts
	for _, analysis := range analyses {
		entry := getCompetitorKeywordEntry{
			Keyword:      analysis.Keyword,
			Kind:         analysis.Kind,
			State:        "uncrawled",
			Matches:      []keywords.Match{},
			Rank:         analysis.Rank,
			Traffic:      analysis.Traffic,
			Volume:       analysis.Volume,
			KD:           analysis.KD,
			CPC:          analysis.CPC,
			ResultsCount: analysis.ResultsCount,
		}
		if coverageAvailable {
			seed, ok := coverByKeyword[strings.ToLower(analysis.Keyword)]
			if !ok {
				seed = keywords.Seed{Keyword: analysis.Keyword, State: keywords.StateNoLandingPage, Matches: []keywords.Match{}}
			}
			matches := seed.Matches
			if matches == nil {
				matches = []keywords.Match{}
			}
			if len(matches) > getKeywordCoverageMaxMatches {
				matches = matches[:getKeywordCoverageMaxMatches]
			}
			entry.State = string(seed.State)
			entry.MatchCount = len(seed.Matches)
			entry.Matches = matches
			switch seed.State {
			case keywords.StateCannibalized:
				counts.Cannibalized++
			case keywords.StateNoLandingPage:
				counts.NoLandingPage++
			default:
				counts.LikelyTargeted++
			}
		}
		entries = append(entries, entry)
	}
	return entries, counts
}

func filterCompetitorAnalyses(analyses []keywordanalysis.Analysis, kind string) []keywordanalysis.Analysis {
	if kind == "all" {
		out := make([]keywordanalysis.Analysis, 0, len(analyses))
		return append(out, analyses...)
	}
	out := make([]keywordanalysis.Analysis, 0, len(analyses))
	for _, analysis := range analyses {
		if analysis.Kind == kind {
			out = append(out, analysis)
		}
	}
	return out
}

// resolveCompetitorArg matches the raw competitor argument to one roster row
// by ID or case-insensitive name substring. A blank argument selects the
// single competitor; with several competitors it asks for disambiguation.
// A non-empty return message is model-facing content, not an infra error.
func resolveCompetitorArg(roster []sqlc.ProjectCompetitor, raw string) (sqlc.ProjectCompetitor, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if len(roster) == 1 {
			return roster[0], "", nil
		}
		return sqlc.ProjectCompetitor{}, getCompetitorKeywordsName + " error: competitor is required: the project has " +
			fmt.Sprintf("%d", len(roster)) + " competitors (" + competitorCandidateList(roster) + "). Pass competitor as a roster id or a name substring", nil
	}
	if id, err := guuid.Parse(trimmed); err == nil {
		for _, competitor := range roster {
			if competitor.ID.Bytes == [16]byte(id) {
				return competitor, "", nil
			}
		}
		return sqlc.ProjectCompetitor{}, getCompetitorKeywordsName + " error: no competitor with id " + trimmed +
			" in this project. Known competitors: " + competitorCandidateList(roster), nil
	}
	needle := strings.ToLower(trimmed)
	var matches []sqlc.ProjectCompetitor
	for _, competitor := range roster {
		if strings.Contains(strings.ToLower(competitor.Name), needle) {
			matches = append(matches, competitor)
		}
	}
	if len(matches) == 0 {
		for _, competitor := range roster {
			if strings.Contains(strings.ToLower(competitor.SeedUrl), needle) {
				matches = append(matches, competitor)
			}
		}
	}
	switch len(matches) {
	case 0:
		return sqlc.ProjectCompetitor{}, getCompetitorKeywordsName + " error: no competitor matches " + fmt.Sprintf("%q", trimmed) +
			". Known competitors: " + competitorCandidateList(roster), nil
	case 1:
		return matches[0], "", nil
	default:
		return sqlc.ProjectCompetitor{}, getCompetitorKeywordsName + " error: " + fmt.Sprintf("%q", trimmed) + " matches " +
			fmt.Sprintf("%d", len(matches)) + " competitors (" + competitorCandidateList(matches) + "). Pass a roster id or a more specific name substring", nil
	}
}

func competitorCandidateList(roster []sqlc.ProjectCompetitor) string {
	names := make([]string, 0, len(roster))
	for _, competitor := range roster {
		names = append(names, competitor.Name+" ("+competitor.ID.String()+")")
	}
	return strings.Join(names, ", ")
}

func parseGetCompetitorKeywordsArgs(raw json.RawMessage) (getCompetitorKeywordsArgs, error) {
	args := getCompetitorKeywordsArgs{Kind: projectkeywords.ProjectKeywordKindNonBrand}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "competitor":
			var competitor string
			if err := json.Unmarshal(value, &competitor); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Competitor = competitor
		case "type":
			var kind string
			if err := json.Unmarshal(value, &kind); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			switch kind {
			case projectkeywords.ProjectKeywordKindBrand, projectkeywords.ProjectKeywordKindNonBrand, "all":
				args.Kind = kind
			default:
				return args, fmt.Errorf("unknown type %q; valid types: brand, non_brand, all", kind)
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}
