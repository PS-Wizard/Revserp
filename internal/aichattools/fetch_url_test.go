package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/tinyfish"
)

// fakeWebClient implements WebClient without any network access.
type fakeWebClient struct {
	fetchResult  tinyfish.FetchResult
	fetchErr     error
	fetchCalls   int
	lastFetchURL string
}

func (f *fakeWebClient) Search(context.Context, string, int) ([]tinyfish.SearchResult, error) {
	return nil, nil
}

func (f *fakeWebClient) Fetch(_ context.Context, rawURL string) (tinyfish.FetchResult, error) {
	f.fetchCalls++
	f.lastFetchURL = rawURL
	return f.fetchResult, f.fetchErr
}

func runFetchURL(t *testing.T, web WebClient, budget *WebBudget, raw string) Result {
	t.Helper()
	result, err := executeFetchURL(context.Background(), json.RawMessage(raw), Scope{Web: web, WebBudget: budget})
	if err != nil {
		t.Fatalf("executeFetchURL(%s) returned error: %v", raw, err)
	}
	return result
}

func TestFetchURLDef(t *testing.T) {
	tool := fetchURLTool()
	if tool.Def.Name != "fetch_url" || tool.Def.Label == "" {
		t.Fatalf("invalid definition: %+v", tool.Def)
	}
	if tool.Def.Feature != "ai_chat" {
		t.Fatalf("Feature = %q, want ai_chat", tool.Def.Feature)
	}
	if tool.Def.Description == "" || !json.Valid(tool.Def.Schema) {
		t.Fatalf("definition incomplete: %+v", tool.Def)
	}
	var schema struct {
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "url" || schema.AdditionalProperties {
		t.Fatalf("schema = %+v", schema)
	}
}

func TestFetchURLHappyPath(t *testing.T) {
	web := &fakeWebClient{fetchResult: tinyfish.FetchResult{
		URL:           "https://example.com/pricing",
		FinalURL:      "https://example.com/pricing",
		Title:         "Pricing",
		PublishedDate: "2024-05-01",
		Format:        "markdown",
		Text:          "# Pricing\n\nOur plans.",
	}}
	result := runFetchURL(t, web, NewWebBudget(3, 3), `{"url":"https://example.com/pricing"}`)
	want := "Pricing\nhttps://example.com/pricing\n2024-05-01\n\n# Pricing\n\nOur plans."
	if result.Content != want {
		t.Fatalf("Content = %q, want %q", result.Content, want)
	}
	if result.Summary != "Fetched example.com/pricing" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "Fetched example.com/pricing")
	}
	if web.fetchCalls != 1 || web.lastFetchURL != "https://example.com/pricing" {
		t.Fatalf("fetch calls=%d url=%q", web.fetchCalls, web.lastFetchURL)
	}
}

func TestFetchURLFallsBackToURLWhenTitleEmpty(t *testing.T) {
	web := &fakeWebClient{fetchResult: tinyfish.FetchResult{
		URL:      "https://example.com/a",
		FinalURL: "https://example.com/a",
		Text:     "body",
	}}
	result := runFetchURL(t, web, NewWebBudget(3, 3), `{"url":"https://example.com/a"}`)
	if !strings.HasPrefix(result.Content, "https://example.com/a\nhttps://example.com/a\n\n") {
		t.Fatalf("Content = %q, want URL repeated on the first line", result.Content)
	}
}

func TestFetchURLTruncatesLongText(t *testing.T) {
	long := strings.Repeat("word ", 8000) // 40 KB, above the 24 KB limit.
	web := &fakeWebClient{fetchResult: tinyfish.FetchResult{
		URL:      "https://example.com/big",
		FinalURL: "https://example.com/big",
		Title:    "Big",
		Text:     long,
	}}
	result := runFetchURL(t, web, NewWebBudget(3, 3), `{"url":"https://example.com/big"}`)
	if !strings.HasSuffix(result.Content, fetchURLTruncationNote) {
		t.Fatalf("truncation note missing: %q", result.Content)
	}
	body := strings.TrimPrefix(result.Content, "Big\nhttps://example.com/big\n\n")
	body = strings.TrimSuffix(body, fetchURLTruncationNote)
	if len(body) != fetchURLTextLimit {
		t.Fatalf("capped body = %d bytes, want %d", len(body), fetchURLTextLimit)
	}
}

func TestFetchURLEmptyText(t *testing.T) {
	web := &fakeWebClient{fetchResult: tinyfish.FetchResult{
		URL:      "https://example.com/blank",
		FinalURL: "https://example.com/blank",
		Title:    "Blank",
	}}
	result := runFetchURL(t, web, NewWebBudget(3, 3), `{"url":"https://example.com/blank"}`)
	if !strings.Contains(result.Content, "That page returned no readable text.") {
		t.Fatalf("Content = %q, want empty-text message", result.Content)
	}
	if !strings.Contains(result.Content, "Blank") || !strings.Contains(result.Content, "https://example.com/blank") {
		t.Fatalf("Content = %q, want title and URL", result.Content)
	}
}

func TestFetchURLProviderError(t *testing.T) {
	web := &fakeWebClient{fetchErr: errors.New("provider exploded")}
	result := runFetchURL(t, web, NewWebBudget(3, 3), `{"url":"https://example.com/a"}`)
	if result.Content != "Could not fetch that URL right now." {
		t.Fatalf("Content = %q, want generic failure message", result.Content)
	}
	if strings.Contains(result.Content, "exploded") {
		t.Fatal("Content leaked the provider error")
	}
}

func TestFetchURLUnavailableWithoutWebClient(t *testing.T) {
	budget := NewWebBudget(3, 1)
	result := runFetchURL(t, nil, budget, `{"url":"https://example.com/a"}`)
	if !strings.Contains(result.Content, "Web fetching is not available") {
		t.Fatalf("Content = %q, want unavailable message", result.Content)
	}
	if result.Summary == "" {
		t.Fatal("Summary is empty, want a UI caption")
	}
	// A nil client must not spend budget; the one fetch is still available.
	if err := budget.SpendFetch(); err != nil {
		t.Fatalf("budget was spent without a web client: %v", err)
	}
}

func TestFetchURLBudgetExhausted(t *testing.T) {
	web := &fakeWebClient{fetchResult: tinyfish.FetchResult{
		URL: "https://example.com/a", FinalURL: "https://example.com/a", Title: "A", Text: "ok",
	}}
	budget := NewWebBudget(3, 3)
	for i := 0; i < 3; i++ {
		runFetchURL(t, web, budget, `{"url":"https://example.com/a"}`)
	}
	result := runFetchURL(t, web, budget, `{"url":"https://example.com/a"}`)
	if result.Content != "Fetch limit reached for this turn." {
		t.Fatalf("Content = %q, want limit message", result.Content)
	}
	if web.fetchCalls != 3 {
		t.Fatalf("provider called %d times, want 3", web.fetchCalls)
	}
}

func TestFetchURLValidationRejectsBeforeBudget(t *testing.T) {
	tests := []string{
		"ftp://example.com",
		"javascript:alert(1)",
		"http://localhost/x",
		"http://127.0.0.1/x",
		"http://10.0.0.1/x",
		"http://user:pass@example.com/x",
		"http://example.com:8080/x",
		"http:///no-host",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			web := &fakeWebClient{}
			budget := NewWebBudget(3, 3)
			args, err := json.Marshal(map[string]string{"url": raw})
			if err != nil {
				t.Fatal(err)
			}
			_, err = executeFetchURL(context.Background(), args, Scope{Web: web, WebBudget: budget})
			if err == nil {
				t.Fatalf("executeFetchURL(%s) succeeded, want an error", raw)
			}
			if web.fetchCalls != 0 {
				t.Fatalf("provider called %d times for a rejected url", web.fetchCalls)
			}
			// The budget must be untouched: a fetch is still available.
			if err := budget.SpendFetch(); err != nil {
				t.Fatalf("budget was spent on a rejected url: %v", err)
			}
		})
	}
}
