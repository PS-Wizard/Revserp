package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/tinyfish"
)

// fakeWebSearchClient implements WebClient without a network. Fetch is unused by the
// search tool, so it fails loudly if the tool ever calls it.
type fakeWebSearchClient struct {
	results []tinyfish.SearchResult
	err     error
	calls   int
	queries []string
	limits  []int
}

func (f *fakeWebSearchClient) Search(_ context.Context, query string, limit int) ([]tinyfish.SearchResult, error) {
	f.calls++
	f.queries = append(f.queries, query)
	f.limits = append(f.limits, limit)
	return f.results, f.err
}

func (f *fakeWebSearchClient) Fetch(_ context.Context, _ string) (tinyfish.FetchResult, error) {
	return tinyfish.FetchResult{}, errors.New("Fetch must not be called by web_search")
}

func runWebSearch(t *testing.T, raw string, scope Scope) (Result, error) {
	t.Helper()
	return executeWebSearch(context.Background(), json.RawMessage(raw), scope)
}

func webSearchResults(t *testing.T, count int) []tinyfish.SearchResult {
	t.Helper()
	results := make([]tinyfish.SearchResult, count)
	for i := range results {
		results[i] = tinyfish.SearchResult{
			Position: i + 1,
			Title:    "Title " + string(rune('A'+i)),
			URL:      "https://example.com/" + string(rune('a'+i)),
			Snippet:  "Snippet for result",
		}
	}
	return results
}

func TestWebSearchToolDef(t *testing.T) {
	tool := webSearchTool()
	if tool.Def.Name != webSearchName || tool.Def.Label != "Search the web" {
		t.Fatalf("def = %+v", tool.Def)
	}
	if tool.Def.Feature != "ai_chat" {
		t.Fatalf("Feature = %q, want ai_chat", tool.Def.Feature)
	}
	var schema struct {
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "query" || schema.AdditionalProperties {
		t.Fatalf("schema = %+v", schema)
	}
}

func TestWebSearchHappyPathFormatting(t *testing.T) {
	fake := &fakeWebSearchClient{results: []tinyfish.SearchResult{
		{Position: 1, Title: "  Go   regexp  docs ", URL: "https://pkg.go.dev/regexp", Snippet: "The regexp package\nimplements regular expressions."},
		{Position: 2, Title: "RE2", URL: "https://github.com/google/re2", Snippet: "Fast"},
	}}
	result, err := runWebSearch(t, `{"query":"golang regex"}`, Scope{Web: fake})
	if err != nil {
		t.Fatal(err)
	}
	wantContent := "1. Go regexp docs\nhttps://pkg.go.dev/regexp\nThe regexp package implements regular expressions.\n\n2. RE2\nhttps://github.com/google/re2\nFast"
	if result.Content != wantContent {
		t.Fatalf("Content = %q\nwant        %q", result.Content, wantContent)
	}
	if want := `Searched the web for "golang regex" · 2 results`; result.Summary != want {
		t.Fatalf("Summary = %q, want %q", result.Summary, want)
	}
	if fake.queries[0] != "golang regex" || fake.limits[0] != webSearchDefaultResults {
		t.Fatalf("provider call = query %q limit %d", fake.queries[0], fake.limits[0])
	}
}

func TestWebSearchLimitClamping(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{raw: `{"query":"x"}`, want: webSearchDefaultResults},
		{raw: `{"query":"x","limit":0}`, want: webSearchDefaultResults},
		{raw: `{"query":"x","limit":99}`, want: webSearchMaxResults},
		{raw: `{"query":"x","limit":3}`, want: 3},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			fake := &fakeWebSearchClient{results: webSearchResults(t, 1)}
			if _, err := runWebSearch(t, test.raw, Scope{Web: fake}); err != nil {
				t.Fatal(err)
			}
			if len(fake.limits) != 1 || fake.limits[0] != test.want {
				t.Fatalf("provider limit = %v, want %d", fake.limits, test.want)
			}
		})
	}
}

func TestWebSearchSnippetTruncation(t *testing.T) {
	long := strings.Repeat("a", 400)
	fake := &fakeWebSearchClient{results: []tinyfish.SearchResult{
		{Position: 1, Title: "T", URL: "https://example.com/a", Snippet: long},
	}}
	result, err := runWebSearch(t, `{"query":"x"}`, Scope{Web: fake})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("a", webSearchMaxSnippetLength) + "\u2026"
	if !strings.Contains(result.Content, want) {
		t.Fatalf("Content did not truncate the snippet with an ellipsis: %q", result.Content)
	}
	if strings.Contains(result.Content, long) {
		t.Fatal("Content still holds the untruncated snippet")
	}
}

func TestWebSearchOmittedResultsNote(t *testing.T) {
	fake := &fakeWebSearchClient{results: webSearchResults(t, 7)}
	result, err := runWebSearch(t, `{"query":"x","limit":3}`, Scope{Web: fake})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "4 more results omitted.") {
		t.Fatalf("Content = %q, want omitted note", result.Content)
	}
	if want := `Searched the web for "x" · 3 results`; result.Summary != want {
		t.Fatalf("Summary = %q, want %q", result.Summary, want)
	}
}

func TestWebSearchUnavailableWithoutClient(t *testing.T) {
	result, err := runWebSearch(t, `{"query":"x"}`, Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Web search is not available for this project." || result.Summary == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestWebSearchBudgetExhaustion(t *testing.T) {
	fake := &fakeWebSearchClient{results: webSearchResults(t, 1)}
	budget := NewWebBudget(3, 3)
	scope := Scope{Web: fake, WebBudget: budget}
	for i := 0; i < 3; i++ {
		result, err := runWebSearch(t, `{"query":"x"}`, scope)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content == "Web search limit reached for this turn." {
			t.Fatalf("call %d was denied early", i+1)
		}
	}
	result, err := runWebSearch(t, `{"query":"x"}`, scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Web search limit reached for this turn." {
		t.Fatalf("4th call Content = %q, want limit message", result.Content)
	}
	if fake.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (no call after exhaustion)", fake.calls)
	}
}

func TestWebSearchProviderErrorIsSoft(t *testing.T) {
	fake := &fakeWebSearchClient{err: errors.New("boom: internal provider detail")}
	result, err := runWebSearch(t, `{"query":"x"}`, Scope{Web: fake})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Web search is unavailable right now." {
		t.Fatalf("Content = %q", result.Content)
	}
	if strings.Contains(result.Content, "boom") {
		t.Fatal("Content leaked the underlying provider error")
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	fake := &fakeWebSearchClient{}
	result, err := runWebSearch(t, `{"query":"x"}`, Scope{Web: fake})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "No results for that query." {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestWebSearchMissingQueryIsError(t *testing.T) {
	fake := &fakeWebSearchClient{results: webSearchResults(t, 1)}
	for _, raw := range []string{`{}`, `{"query":""}`, `{"query":"   "}`} {
		if _, err := runWebSearch(t, raw, Scope{Web: fake}); err == nil {
			t.Errorf("runWebSearch(%s) succeeded, want missing-query error", raw)
		}
	}
	if fake.calls != 0 {
		t.Fatalf("provider was called %d times on broken input", fake.calls)
	}
}
