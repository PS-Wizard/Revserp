package aichattools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
)

func int32Ptr(v int32) *int32 { return &v }

func int64Ptr(v int64) *int64 { return &v }

func newGetCompetitorKeywordsExecutor(store *fakeCompetitorRosterStore, analyses *fakeCompetitorAnalyses) *getCompetitorKeywordsExecutor {
	return &getCompetitorKeywordsExecutor{store: store, analyses: analyses}
}

func decodeGetCompetitorKeywords(t *testing.T, result Result) getCompetitorKeywordsResponse {
	t.Helper()
	var response getCompetitorKeywordsResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not get_competitor_keywords JSON: %v\n%s", err, result.Content)
	}
	return response
}

func competitorKeywordStore() *fakeCompetitorRosterStore {
	return &fakeCompetitorRosterStore{
		project: sqlc.Project{ID: testProjectID},
		roster:  testCompetitorRoster(),
		parent:  testParentCrawlID,
		children: map[string]sqlc.GetCompetitorChildCrawlForParentRow{
			testCompetitorAID.String(): {ID: testChildCrawlID, Status: "completed"},
		},
		pages: []sqlc.ListKeywordCoveragePagesForCrawlRow{
			{Url: "https://rival.example/plumbing-repair", Title: "Plumbing Repair Experts | Rival", H1: "Plumbing Repair", StatusCode: 200, ContentType: "text/html"},
			{Url: "https://rival.example/about", Title: "About Rival Plumbing", H1: "Our Story", StatusCode: 200, ContentType: "text/html"},
		},
	}
}

func competitorKeywordAnalyses() *fakeCompetitorAnalyses {
	return &fakeCompetitorAnalyses{byCompetitor: map[string][]keywordanalysis.Analysis{
		testCompetitorAID.String(): {
			{Keyword: "Rival", Kind: "brand", Rank: int32Ptr(2), Traffic: int64Ptr(40)},
			{Keyword: "plumbing repair", Kind: "non_brand", Rank: int32Ptr(3), Traffic: int64Ptr(120)},
			{Keyword: "drain cleaning", Kind: "non_brand", Rank: int32Ptr(9)},
		},
	}}
}

func TestParseGetCompetitorKeywordsArgs(t *testing.T) {
	args, err := parseGetCompetitorKeywordsArgs(json.RawMessage(`{}`))
	if err != nil || args.Kind != "non_brand" || args.Competitor != "" {
		t.Fatalf("defaults = %+v, %v; want non_brand with no competitor", args, err)
	}
	args, err = parseGetCompetitorKeywordsArgs(json.RawMessage(`{"competitor":"Rival","type":"all"}`))
	if err != nil || args.Kind != "all" || args.Competitor != "Rival" {
		t.Fatalf("parsed = %+v, %v; want competitor Rival with type all", args, err)
	}
	for _, raw := range []string{
		`{"bogus":1}`,
		`{"project_id":"x"}`,
		`{"type":"gap"}`,
		`{"type":""}`,
		`{"type":1}`,
		`{"competitor":5}`,
	} {
		if _, err := parseGetCompetitorKeywordsArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("args %s: got nil error, want rejection", raw)
		}
	}
}

func TestGetCompetitorKeywordsAccessDenied(t *testing.T) {
	store := &fakeCompetitorRosterStore{accessErr: pgx.ErrNoRows}
	exec := newGetCompetitorKeywordsExecutor(store, &fakeCompetitorAnalyses{})
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(result.Content, "project not found or access denied") {
		t.Fatalf("content = %q, want access denial", result.Content)
	}
}

func TestGetCompetitorKeywordsNoRoster(t *testing.T) {
	store := &fakeCompetitorRosterStore{project: sqlc.Project{ID: testProjectID}}
	exec := newGetCompetitorKeywordsExecutor(store, &fakeCompetitorAnalyses{})
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(result.Content, "no competitors saved yet") {
		t.Fatalf("content = %q, want empty-roster guidance", result.Content)
	}
}

func TestGetCompetitorKeywordsDefaultsToNonBrandWithCoverage(t *testing.T) {
	exec := newGetCompetitorKeywordsExecutor(competitorKeywordStore(), competitorKeywordAnalyses())
	result, err := exec.run(context.Background(), json.RawMessage(`{"competitor":"rival"}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeGetCompetitorKeywords(t, result)
	if response.Type != "non_brand" || response.TotalKeywords != 2 {
		t.Fatalf("response = %+v, want 2 non_brand keywords", response)
	}
	if !response.CoverageAvailable || response.CrawlID == nil || *response.CrawlID != testChildCrawlID.String() {
		t.Fatalf("response = %+v, want coverage on the child crawl", response)
	}
	byKeyword := map[string]getCompetitorKeywordEntry{}
	for _, entry := range response.Keywords {
		byKeyword[entry.Keyword] = entry
	}
	covered, ok := byKeyword["plumbing repair"]
	if !ok || covered.State != "likely_targeted" || covered.MatchCount == 0 {
		t.Fatalf("plumbing repair = %+v, want likely_targeted with matches", covered)
	}
	if len(covered.Matches) == 0 || !strings.Contains(covered.Matches[0].URL, "plumbing-repair") {
		t.Fatalf("plumbing repair matches = %+v, want the repair page URL", covered.Matches)
	}
	if covered.Rank == nil || *covered.Rank != 3 {
		t.Fatalf("plumbing repair rank = %+v, want stored estimate 3", covered.Rank)
	}
	gap, ok := byKeyword["drain cleaning"]
	if !ok || gap.State != "no_landing_page" || gap.MatchCount != 0 {
		t.Fatalf("drain cleaning = %+v, want a gap with no matches", gap)
	}
	if gap.Matches == nil {
		t.Fatal("gap matches must be a non-null array")
	}
	if response.Counts.LikelyTargeted != 1 || response.Counts.NoLandingPage != 1 || response.Counts.Cannibalized != 0 {
		t.Fatalf("counts = %+v, want 1 targeted and 1 gap", response.Counts)
	}
	if !strings.Contains(result.Summary, "1 targeted, 1 gaps, 0 cannibalized") {
		t.Fatalf("summary = %q, want coverage counts", result.Summary)
	}
}

func TestGetCompetitorKeywordsTypeFilter(t *testing.T) {
	exec := newGetCompetitorKeywordsExecutor(competitorKeywordStore(), competitorKeywordAnalyses())
	brand, err := exec.run(context.Background(), json.RawMessage(`{"competitor":"Rival Plumbing","type":"brand"}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	brandResponse := decodeGetCompetitorKeywords(t, brand)
	if brandResponse.TotalKeywords != 1 || brandResponse.Keywords[0].Keyword != "Rival" {
		t.Fatalf("brand response = %+v, want only the brand keyword", brandResponse)
	}
	all, err := exec.run(context.Background(), json.RawMessage(fmt.Sprintf(`{"competitor":%q,"type":"all"}`, testCompetitorAID.String())), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if allResponse := decodeGetCompetitorKeywords(t, all); allResponse.TotalKeywords != 3 {
		t.Fatalf("all response = %+v, want all 3 keywords", allResponse)
	}
}

func TestGetCompetitorKeywordsDisambiguation(t *testing.T) {
	store := &fakeCompetitorRosterStore{project: sqlc.Project{ID: testProjectID}, roster: testCompetitorRoster()}
	exec := newGetCompetitorKeywordsExecutor(store, &fakeCompetitorAnalyses{})
	missing, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(missing.Content, "competitor is required") || !strings.Contains(missing.Content, "Rival Plumbing") || !strings.Contains(missing.Content, "Apex Drains") {
		t.Fatalf("content = %q, want disambiguation listing both competitors", missing.Content)
	}
	ambiguous, err := exec.run(context.Background(), json.RawMessage(`{"competitor":"a"}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(ambiguous.Content, "matches 2 competitors") {
		t.Fatalf("content = %q, want ambiguity notice", ambiguous.Content)
	}
	unknown, err := exec.run(context.Background(), json.RawMessage(`{"competitor":"Nobody"}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(unknown.Content, "no competitor matches") {
		t.Fatalf("content = %q, want not-found notice", unknown.Content)
	}
}

func TestGetCompetitorKeywordsSingleCompetitorNeedsNoArg(t *testing.T) {
	store := &fakeCompetitorRosterStore{
		project: sqlc.Project{ID: testProjectID},
		roster:  testCompetitorRoster()[:1],
		parent:  testParentCrawlID,
		children: map[string]sqlc.GetCompetitorChildCrawlForParentRow{
			testCompetitorAID.String(): {ID: testChildCrawlID, Status: "completed"},
		},
	}
	exec := newGetCompetitorKeywordsExecutor(store, competitorKeywordAnalyses())
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeGetCompetitorKeywords(t, result)
	if response.Competitor.Name != "Rival Plumbing" {
		t.Fatalf("response = %+v, want the single competitor selected", response)
	}
}

func TestGetCompetitorKeywordsNotCrawledListsWithoutCoverage(t *testing.T) {
	store := competitorKeywordStore()
	store.children = map[string]sqlc.GetCompetitorChildCrawlForParentRow{}
	exec := newGetCompetitorKeywordsExecutor(store, competitorKeywordAnalyses())
	result, err := exec.run(context.Background(), json.RawMessage(`{"competitor":"rival"}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeGetCompetitorKeywords(t, result)
	if response.CoverageAvailable || response.CrawlID != nil {
		t.Fatalf("response = %+v, want no coverage without a child crawl", response)
	}
	if response.Note == "" {
		t.Fatal("response must explain why coverage is missing")
	}
	for _, entry := range response.Keywords {
		if entry.State != "uncrawled" {
			t.Fatalf("entry = %+v, want uncrawled state", entry)
		}
	}
	if !strings.Contains(result.Summary, "not crawled yet") {
		t.Fatalf("summary = %q, want not-crawled marker", result.Summary)
	}
}

func TestGetCompetitorKeywordsCapsAtFifty(t *testing.T) {
	var analyses []keywordanalysis.Analysis
	for i := range 60 {
		analyses = append(analyses, keywordanalysis.Analysis{Keyword: fmt.Sprintf("keyword %02d", i), Kind: "non_brand"})
	}
	store := &fakeCompetitorRosterStore{
		project:   sqlc.Project{ID: testProjectID},
		roster:    testCompetitorRoster()[:1],
		parentErr: pgx.ErrNoRows,
	}
	fakes := &fakeCompetitorAnalyses{byCompetitor: map[string][]keywordanalysis.Analysis{testCompetitorAID.String(): analyses}}
	exec := newGetCompetitorKeywordsExecutor(store, fakes)
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeGetCompetitorKeywords(t, result)
	if response.TotalKeywords != 50 || !response.Truncated || len(response.Keywords) != 50 {
		t.Fatalf("total/truncated/keywords = %d/%v/%d, want 50/true/50", response.TotalKeywords, response.Truncated, len(response.Keywords))
	}
}

func TestExecuteGetCompetitorKeywordsDeniesLocation(t *testing.T) {
	scope := Scope{LocationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}
	result, err := executeGetCompetitorKeywords(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("location denial must be model-visible, got err %v", err)
	}
	if !strings.HasPrefix(result.Content, "get_competitor_keywords error:") || !strings.Contains(result.Content, "location") {
		t.Fatalf("content = %q, want location denial", result.Content)
	}
}
