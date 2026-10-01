package aichattools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywords"
)

type fakeKeywordCoverageStore struct {
	project      sqlc.Project
	accessErr    error
	profile      sqlc.GetProjectBusinessProfileByProjectIDForUserRow
	profileErr   error
	crawlID      pgtype.UUID
	crawlErr     error
	pages        []sqlc.ListKeywordCoveragePagesForCrawlRow
	pagesErr     error
	profileCalls int
	crawlCalls   int
	pagesCalls   int
	keywordCalls int
	coverCalls   int
	coverSeeds   []keywords.Seed
	keywordLists *fakeProjectKeywordService
}

func (f *fakeKeywordCoverageStore) GetProjectByIDForUser(_ context.Context, _ sqlc.GetProjectByIDForUserParams) (sqlc.Project, error) {
	if f.accessErr != nil {
		return sqlc.Project{}, f.accessErr
	}
	return f.project, nil
}

func (f *fakeKeywordCoverageStore) GetProjectBusinessProfileByProjectIDForUser(_ context.Context, _ sqlc.GetProjectBusinessProfileByProjectIDForUserParams) (sqlc.GetProjectBusinessProfileByProjectIDForUserRow, error) {
	f.profileCalls++
	if f.profileErr != nil {
		return sqlc.GetProjectBusinessProfileByProjectIDForUserRow{}, f.profileErr
	}
	return f.profile, nil
}

func (f *fakeKeywordCoverageStore) GetLatestCompletedCrawlForProject(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	f.crawlCalls++
	if f.crawlErr != nil {
		return pgtype.UUID{}, f.crawlErr
	}
	return f.crawlID, nil
}

func (f *fakeKeywordCoverageStore) ListKeywordCoveragePagesForCrawl(_ context.Context, _ pgtype.UUID) ([]sqlc.ListKeywordCoveragePagesForCrawlRow, error) {
	f.pagesCalls++
	if f.pagesErr != nil {
		return nil, f.pagesErr
	}
	return f.pages, nil
}

func coverageTestSeeds() []keywords.Seed {
	matches := func(urls ...string) []keywords.Match {
		out := make([]keywords.Match, 0, len(urls))
		for _, url := range urls {
			out = append(out, keywords.Match{URL: url, Field: keywords.FieldTitle})
		}
		return out
	}
	return []keywords.Seed{
		{Keyword: "zebra tours", State: keywords.StateLikelyTargeted, Matches: matches("https://x.test/zebra")},
		{Keyword: "Alpha Plumbing", State: keywords.StateCannibalized, Matches: matches("https://x.test/a1", "https://x.test/a2")},
		{Keyword: "beta plumbing", State: keywords.StateNoLandingPage, Matches: []keywords.Match{}},
		{Keyword: "alpha services", State: keywords.StateLikelyTargeted, Matches: matches("https://x.test/s")},
	}
}

func newCoverageTestExecutor(store *fakeKeywordCoverageStore) *keywordCoverageExecutor {
	return &keywordCoverageExecutor{
		store:    store,
		keywords: store.keywordLists,
		cover: func(_ []keywords.Page, _ []string, _ string) []keywords.Seed {
			store.coverCalls++
			return coverageTestSeeds()
		},
		memo: &keywordCoverageMemo{},
	}
}

func coverageTestStore() *fakeKeywordCoverageStore {
	return &fakeKeywordCoverageStore{
		project:      sqlc.Project{ID: testProjectID},
		crawlID:      testCrawlID,
		profileErr:   pgx.ErrNoRows,
		keywordLists: newFakeProjectKeywordService(),
	}
}

func decodeKeywordCoverage(t *testing.T, result Result) keywordCoverageResponse {
	t.Helper()
	var response keywordCoverageResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("decode coverage response %q: %v", result.Content, err)
	}
	return response
}

func TestParseKeywordCoverageArgs(t *testing.T) {
	args, err := parseKeywordCoverageArgs(json.RawMessage(`{}`))
	if err != nil || args.Limit != 50 || args.State != "" || args.Search != "" {
		t.Fatalf("defaults = %+v, %v; want limit 50 with no filters", args, err)
	}
	args, err = parseKeywordCoverageArgs(json.RawMessage(`{"state":"cannibalized","search":"Plumb","limit":300}`))
	if err != nil || args.State != "cannibalized" || args.Search != "Plumb" || args.Limit != 250 {
		t.Fatalf("clamped = %+v, %v; want state cannibalized, search Plumb, limit 250", args, err)
	}
	for _, raw := range []string{
		`{"bogus":1}`,
		`{"project_id":"x"}`,
		`{"state":"gap"}`,
		`{"state":""}`,
		`{"state":1}`,
		`{"limit":0}`,
		`{"limit":-3}`,
		`{"limit":1.5}`,
		`{"limit":"50"}`,
		`{"search":5}`,
	} {
		if _, err := parseKeywordCoverageArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("args %s: got nil error, want rejection", raw)
		}
	}
}

func TestGetKeywordCoverageFilters(t *testing.T) {
	exec := newCoverageTestExecutor(coverageTestStore())
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeKeywordCoverage(t, result)
	if response.TotalSeeds != 4 || response.Truncated {
		t.Fatalf("total/truncated = %d/%v, want 4/false", response.TotalSeeds, response.Truncated)
	}
	if response.Counts.Cannibalized != 1 || response.Counts.NoLandingPage != 1 || response.Counts.LikelyTargeted != 2 {
		t.Fatalf("counts = %+v, want 1/1/2", response.Counts)
	}
	// Sorted cannibalized, then gaps, then targeted, then alphabetical.
	wantOrder := []string{"Alpha Plumbing", "beta plumbing", "alpha services", "zebra tours"}
	for i, want := range wantOrder {
		if response.Seeds[i].Keyword != want {
			t.Fatalf("seeds order = %v, want %v", response.Seeds, wantOrder)
		}
	}
	if *response.CrawlID != testCrawlID.String() {
		t.Fatalf("crawl_id = %s, want %s", *response.CrawlID, testCrawlID.String())
	}
	if !strings.HasPrefix(result.Summary, "coverage: 2 targeted, 1 gaps, 1 cannibalized") {
		t.Fatalf("summary = %q", result.Summary)
	}

	stateOnly, err := exec.run(context.Background(), json.RawMessage(`{"state":"cannibalized"}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stateResponse := decodeKeywordCoverage(t, stateOnly)
	if len(stateResponse.Seeds) != 1 || stateResponse.Seeds[0].State != "cannibalized" {
		t.Fatalf("state filter seeds = %+v, want only the cannibalized seed", stateResponse.Seeds)
	}
	// Counts stay unfiltered even when a filter is applied.
	if stateResponse.Counts.Cannibalized != 1 || stateResponse.Counts.LikelyTargeted != 2 {
		t.Fatalf("filtered counts = %+v, want unfiltered 1/1/2", stateResponse.Counts)
	}

	search, err := exec.run(context.Background(), json.RawMessage(`{"search":"ALPHA"}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	searchResponse := decodeKeywordCoverage(t, search)
	if len(searchResponse.Seeds) != 2 {
		t.Fatalf("search seeds = %+v, want the two alpha keywords", searchResponse.Seeds)
	}

	limited, err := exec.run(context.Background(), json.RawMessage(`{"limit":2}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limitedResponse := decodeKeywordCoverage(t, limited)
	if len(limitedResponse.Seeds) != 2 || !limitedResponse.Truncated {
		t.Fatalf("limited = %+v, want 2 seeds with truncated", limitedResponse)
	}
	if limitedResponse.TotalSeeds != 4 {
		t.Fatalf("limited total = %d, want 4", limitedResponse.TotalSeeds)
	}
}

func TestGetKeywordCoverageNoCrawl(t *testing.T) {
	store := coverageTestStore()
	store.crawlErr = pgx.ErrNoRows
	exec := newCoverageTestExecutor(store)
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(result.Content), "no completed crawl") {
		t.Fatalf("content = %q, want the no-completed-crawl message", result.Content)
	}
}

func TestGetKeywordCoverageAccessDenied(t *testing.T) {
	store := coverageTestStore()
	store.accessErr = pgx.ErrNoRows
	exec := newCoverageTestExecutor(store)
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "project not found or access denied") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestGetKeywordCoverageMemoizesPerTurn(t *testing.T) {
	store := coverageTestStore()
	memo := &keywordCoverageMemo{}
	exec := &keywordCoverageExecutor{store: store, keywords: store.keywordLists, memo: memo}
	exec.cover = func(_ []keywords.Page, _ []string, _ string) []keywords.Seed {
		store.coverCalls++
		return coverageTestSeeds()
	}
	for _, raw := range []string{`{}`, `{"state":"cannibalized"}`, `{"search":"alpha"}`} {
		if _, err := exec.run(context.Background(), json.RawMessage(raw), testProjectID, testUserID, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if store.coverCalls != 1 {
		t.Fatalf("cover calls = %d, want 1: the matrix must compute once per turn", store.coverCalls)
	}
	if store.pagesCalls != 1 || store.profileCalls != 1 {
		t.Fatalf("pages/profile calls = %d/%d, want 1/1", store.pagesCalls, store.profileCalls)
	}
}

func TestGetKeywordCoverageMatchCap(t *testing.T) {
	store := coverageTestStore()
	matches := make([]keywords.Match, 0, 12)
	for i := range 12 {
		matches = append(matches, keywords.Match{URL: "https://x.test/p" + string(rune('a'+i)), Field: keywords.FieldH1})
	}
	exec := &keywordCoverageExecutor{
		store:    store,
		keywords: store.keywordLists,
		cover: func(_ []keywords.Page, _ []string, _ string) []keywords.Seed {
			return []keywords.Seed{{Keyword: "busy", State: keywords.StateCannibalized, Matches: matches}}
		},
		memo: &keywordCoverageMemo{},
	}
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeKeywordCoverage(t, result)
	if len(response.Seeds) != 1 {
		t.Fatalf("seeds = %+v, want one", response.Seeds)
	}
	if response.Seeds[0].MatchCount != 12 || len(response.Seeds[0].Matches) != 10 {
		t.Fatalf("match_count/matches = %d/%d, want 12/10", response.Seeds[0].MatchCount, len(response.Seeds[0].Matches))
	}
}

func TestGetKeywordCoverageRowBudget(t *testing.T) {
	exec := newCoverageTestExecutor(coverageTestStore())
	exhausted, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID, nil, NewBudget(0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exhausted.Content, "row budget") {
		t.Fatalf("exhausted content = %q, want the budget message", exhausted.Content)
	}
	budget := NewBudget(200)
	if _, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID, nil, budget); err != nil {
		t.Fatal(err)
	}
	// Only the 4 returned seeds are spent, not the scanned pages.
	if got := budget.Remaining(); got != 196 {
		t.Fatalf("remaining = %d, want 196", got)
	}
}

func TestGetKeywordCoverageToolBinding(t *testing.T) {
	tool := getKeywordCoverageTool()
	if tool.Def.Name != "get_keyword_coverage" || tool.Def.Label != "Get keyword coverage" {
		t.Fatalf("tool def = %+v", tool.Def)
	}
	if tool.Def.Feature != "" {
		t.Fatalf("feature = %q, want none", tool.Def.Feature)
	}
	if strings.TrimSpace(tool.Def.Description) == "" {
		t.Fatal("description is empty")
	}
	for _, want := range []string{"latest completed crawl", "combined keyword", "likely_targeted", "no_landing_page", "cannibalized", "250"} {
		if !strings.Contains(tool.Def.Description, want) {
			t.Fatalf("description missing %q: %q", want, tool.Def.Description)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if tool.Execute == nil {
		t.Fatal("Execute = nil")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`), Scope{}); err == nil {
		t.Fatal("Execute without queries: got nil error, want no-queries error")
	}
}
