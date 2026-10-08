package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

const getKeywordCoverageName = "get_keyword_coverage"

const (
	getKeywordCoverageDefaultLimit = 50
	getKeywordCoverageMaxLimit     = 250
	getKeywordCoverageMaxMatches   = 10
)

const getKeywordCoverageSchema = `{
  "type": "object",
  "properties": {
    "state": {"type": "string", "enum": ["cannibalized", "no_landing_page", "likely_targeted"], "description": "Only return keywords in this state. Omit for all states."},
    "search": {"type": "string", "description": "Case-insensitive substring filter on the keyword text. Omit for all keywords."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 250, "default": 50, "description": "Max keyword seeds to return. Capped at 250."}
  },
  "additionalProperties": false
}`

// keywordCoverageStore is the narrow data surface the tool needs. The profile
// read reuses the membership-scoped businessProfileReader query, so no new
// query was added for the primary location.
type keywordCoverageStore interface {
	projectKeywordAccessChecker
	businessProfileReader
	GetLatestCompletedCrawlForProject(ctx context.Context, projectID pgtype.UUID) (pgtype.UUID, error)
	ListKeywordCoveragePagesForCrawl(ctx context.Context, crawlID pgtype.UUID) ([]sqlc.ListKeywordCoveragePagesForCrawlRow, error)
}

// keywordCoverageMemo holds the computed seed matrix for one turn. NewRegistry
// runs once per turn, so a memo created inside getKeywordCoverageTool dies with
// the turn. Calls run sequentially, but a mutex guards the memo in case that
// changes.
type keywordCoverageMemo struct {
	mu         sync.Mutex
	projectID  pgtype.UUID
	crawlID    pgtype.UUID
	crawlIDStr string
	seeds      []keywords.Seed
	valid      bool
}

func (m *keywordCoverageMemo) get(projectID, crawlID pgtype.UUID) ([]keywords.Seed, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.valid || m.projectID != projectID || m.crawlID != crawlID {
		return nil, "", false
	}
	return m.seeds, m.crawlIDStr, true
}

func (m *keywordCoverageMemo) set(projectID, crawlID pgtype.UUID, crawlIDStr string, seeds []keywords.Seed) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectID = projectID
	m.crawlID = crawlID
	m.crawlIDStr = crawlIDStr
	m.seeds = seeds
	m.valid = true
}

type keywordCoverageExecutor struct {
	store    keywordCoverageStore
	keywords projectKeywordService
	cover    func(pages []keywords.Page, targetKeywords []string, primaryLocation string) []keywords.Seed
	memo     *keywordCoverageMemo
}

func (e *keywordCoverageExecutor) keywordService() projectKeywordService {
	if e.keywords != nil {
		return e.keywords
	}
	return contractProjectKeywordService{}
}

func (e *keywordCoverageExecutor) coverFunc() func([]keywords.Page, []string, string) []keywords.Seed {
	if e.cover != nil {
		return e.cover
	}
	return keywords.Cover
}

func getKeywordCoverageTool() Tool {
	memo := &keywordCoverageMemo{}
	return Tool{
		Def: Def{
			Name:        getKeywordCoverageName,
			Label:       "Get keyword coverage",
			Description: "Read keyword coverage for the current project: every keyword in the project's combined keyword list (user-defined plus REVSerp-suggested) checked against the scoreable pages of the latest completed crawl, the same numbers as the Keywords tab. Each keyword gets one state: likely_targeted means exactly one page matches the keyword in its title or h1 and the keyword looks targeted; no_landing_page means no page matches and the keyword is a content gap; cannibalized means two or more pages match in title or h1 and the pages compete with each other. Use it to answer what the gaps, targeted, and cannibalized keywords are in one call, optionally narrowed by state or a case-insensitive search on the keyword text. Covers at most 250 keywords.",
			Schema:      json.RawMessage(getKeywordCoverageSchema),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			if s.LocationID.Valid {
				return executeGetLocationKeywordCoverage(ctx, args, s)
			}
			if s.Queries == nil {
				return Result{}, errors.New("get_keyword_coverage: scope has no queries")
			}
			exec := &keywordCoverageExecutor{store: s.Queries, memo: memo}
			return exec.run(ctx, args, s.ProjectID, s.UserID, s.Queries, s.RowBudget)
		},
	}
}

// run executes one get_keyword_coverage call. Membership is rechecked through
// the project join even though the scope already carries the project, so a
// stale or forged scope cannot read another project's coverage.
func (e *keywordCoverageExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID, queries *sqlc.Queries, budget *Budget) (Result, error) {
	if budget != nil && budget.Remaining() == 0 {
		return Result{
			Content: "The row budget for this turn is exhausted. Do not call get_keyword_coverage again; synthesize your answer from the data you already have.",
			Summary: "row budget reached",
		}, nil
	}
	args, err := parseKeywordCoverageArgs(raw)
	if err != nil {
		return Result{Content: getKeywordCoverageName + " error: " + err.Error()}, nil
	}
	if _, err := e.store.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{Content: getKeywordCoverageName + " error: project not found or access denied"}, nil
		}
		return Result{}, fmt.Errorf("%s: check project access: %w", getKeywordCoverageName, err)
	}
	// The latest completed crawl, not the turn's crawl: the tool must match
	// the number the user sees on the Keywords tab.
	crawlID, err := e.store.GetLatestCompletedCrawlForProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{
				Content: "get_keyword_coverage: no completed crawl yet for this project, so there is no keyword coverage to report. Ask the user to run a crawl first.",
				Summary: "no completed crawl",
			}, nil
		}
		return Result{}, fmt.Errorf("%s: read latest crawl: %w", getKeywordCoverageName, err)
	}
	seeds, crawlIDStr, err := e.cachedSeeds(ctx, projectID, userID, crawlID, queries)
	if err != nil {
		return Result{}, err
	}

	counts := countKeywordCoverageStates(seeds)
	filtered := filterKeywordCoverageSeeds(seeds, args)
	truncated := len(filtered) > args.Limit
	filtered = filtered[:min(len(filtered), args.Limit)]
	out := shapeKeywordCoverageSeeds(filtered)
	if budget != nil {
		// Spend only the seeds returned to the model, not the crawl pages
		// scanned internally: the page scan is one indexed query doing the
		// tool's own computation, and charging it would exhaust any real
		// budget on the first call.
		budget.Spend(len(out))
	}
	crawlIDOut := crawlIDStr
	response := keywordCoverageResponse{
		CrawlID:    &crawlIDOut,
		Counts:     counts,
		TotalSeeds: len(seeds),
		Seeds:      out,
		Truncated:  truncated,
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", getKeywordCoverageName, err)
	}
	return Result{
		Content: string(content),
		Summary: fmt.Sprintf("coverage: %d targeted, %d gaps, %d cannibalized",
			counts.LikelyTargeted, counts.NoLandingPage, counts.Cannibalized),
	}, nil
}

// cachedSeeds returns the memoized matrix for (projectID, crawlID), computing
// it once per turn through the same pipeline as handleProjectKeywords.
func (e *keywordCoverageExecutor) cachedSeeds(ctx context.Context, projectID, userID, crawlID pgtype.UUID, queries *sqlc.Queries) ([]keywords.Seed, string, error) {
	if seeds, crawlIDStr, ok := e.memo.get(projectID, crawlID); ok {
		return seeds, crawlIDStr, nil
	}
	lists, err := e.keywordService().LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		return nil, "", fmt.Errorf("%s: load keyword lists: %w", getKeywordCoverageName, err)
	}
	targetKeywords := projectkeywords.CombinedKeywordTexts(lists)

	primaryLocation := ""
	profile, err := e.store.GetProjectBusinessProfileByProjectIDForUser(ctx, sqlc.GetProjectBusinessProfileByProjectIDForUserParams{ProjectID: projectID, UserID: userID})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", fmt.Errorf("%s: read business profile: %w", getKeywordCoverageName, err)
		}
	} else {
		primaryLocation = profileText(profile.PrimaryLocation)
	}

	pageRows, err := e.store.ListKeywordCoveragePagesForCrawl(ctx, crawlID)
	if err != nil {
		return nil, "", fmt.Errorf("%s: list coverage pages: %w", getKeywordCoverageName, err)
	}
	pages := keywordCoveragePagesFromRows(pageRows)

	seeds := e.coverFunc()(pages, targetKeywords, primaryLocation)
	if seeds == nil {
		seeds = []keywords.Seed{}
	}
	sortKeywordCoverageSeeds(seeds)
	crawlIDStr := crawlID.String()
	e.memo.set(projectID, crawlID, crawlIDStr, seeds)
	return seeds, crawlIDStr, nil
}

type keywordCoverageArgs struct {
	State  string
	Search string
	Limit  int
}

type keywordCoverageCounts struct {
	Cannibalized   int `json:"cannibalized"`
	NoLandingPage  int `json:"no_landing_page"`
	LikelyTargeted int `json:"likely_targeted"`
}

type keywordCoverageSeed struct {
	Keyword    string           `json:"keyword"`
	State      string           `json:"state"`
	Geo        bool             `json:"geo,omitempty"`
	MatchCount int              `json:"match_count"`
	Matches    []keywords.Match `json:"matches"`
}

type keywordCoverageResponse struct {
	CrawlID    *string               `json:"crawl_id"`
	Counts     keywordCoverageCounts `json:"counts"`
	TotalSeeds int                   `json:"total_seeds"`
	Seeds      []keywordCoverageSeed `json:"seeds"`
	Truncated  bool                  `json:"truncated"`
}

// parseKeywordCoverageArgs parses the tool arguments strictly: unknown keys,
// duplicate keys, and trailing data are rejected. Empty input yields defaults.
func parseKeywordCoverageArgs(raw json.RawMessage) (keywordCoverageArgs, error) {
	args := keywordCoverageArgs{Limit: getKeywordCoverageDefaultLimit}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "state":
			var state string
			if err := json.Unmarshal(value, &state); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			switch state {
			case string(keywords.StateCannibalized), string(keywords.StateNoLandingPage), string(keywords.StateLikelyTargeted):
				args.State = state
			default:
				return args, fmt.Errorf("unknown state %q; valid states: cannibalized, no_landing_page, likely_targeted", state)
			}
		case "search":
			var search string
			if err := json.Unmarshal(value, &search); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Search = strings.TrimSpace(search)
		case "limit":
			var limit int
			if err := json.Unmarshal(value, &limit); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if limit < 1 {
				return args, fmt.Errorf("argument %q must be at least 1", key)
			}
			if limit > getKeywordCoverageMaxLimit {
				limit = getKeywordCoverageMaxLimit
			}
			args.Limit = limit
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

func keywordCoverageStateRank(state keywords.State) int {
	switch state {
	case keywords.StateCannibalized:
		return 0
	case keywords.StateNoLandingPage:
		return 1
	default:
		return 2
	}
}

// sortKeywordCoverageSeeds orders seeds cannibalized, then no_landing_page,
// then likely_targeted, then alphabetical by keyword, matching the UI.
func sortKeywordCoverageSeeds(seeds []keywords.Seed) {
	sort.Slice(seeds, func(i, j int) bool {
		ri, rj := keywordCoverageStateRank(seeds[i].State), keywordCoverageStateRank(seeds[j].State)
		if ri != rj {
			return ri < rj
		}
		li, lj := strings.ToLower(seeds[i].Keyword), strings.ToLower(seeds[j].Keyword)
		if li != lj {
			return li < lj
		}
		return seeds[i].Keyword < seeds[j].Keyword
	})
}

func countKeywordCoverageStates(seeds []keywords.Seed) keywordCoverageCounts {
	var counts keywordCoverageCounts
	for _, seed := range seeds {
		switch seed.State {
		case keywords.StateCannibalized:
			counts.Cannibalized++
		case keywords.StateNoLandingPage:
			counts.NoLandingPage++
		default:
			counts.LikelyTargeted++
		}
	}
	return counts
}

func filterKeywordCoverageSeeds(seeds []keywords.Seed, args keywordCoverageArgs) []keywords.Seed {
	filtered := make([]keywords.Seed, 0, len(seeds))
	needle := strings.ToLower(args.Search)
	for _, seed := range seeds {
		if args.State != "" && string(seed.State) != args.State {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(seed.Keyword), needle) {
			continue
		}
		filtered = append(filtered, seed)
	}
	return filtered
}

func shapeKeywordCoverageSeeds(seeds []keywords.Seed) []keywordCoverageSeed {
	out := make([]keywordCoverageSeed, 0, len(seeds))
	for _, seed := range seeds {
		matches := seed.Matches
		if matches == nil {
			matches = []keywords.Match{}
		}
		if len(matches) > getKeywordCoverageMaxMatches {
			matches = matches[:getKeywordCoverageMaxMatches]
		}
		out = append(out, keywordCoverageSeed{
			Keyword:    seed.Keyword,
			State:      string(seed.State),
			Geo:        seed.Geo,
			MatchCount: len(seed.Matches),
			Matches:    matches,
		})
	}
	return out
}
