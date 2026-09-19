package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/googlesuggest"
)

// suggestRequestsTestLimit mirrors the worker's per-turn request allowance.
const suggestRequestsTestLimit = 40

type fakeSuggestClient struct {
	suggestResult []googlesuggest.Suggestion
	suggestErr    error
	expandResult  []googlesuggest.Suggestion
	expandErr     error

	suggestCalls    int
	expandCalls     int
	lastSeed        string
	lastMaxRequests int
	lastOpts        googlesuggest.Options
}

func (f *fakeSuggestClient) Suggest(ctx context.Context, seed string, opts googlesuggest.Options) ([]googlesuggest.Suggestion, error) {
	f.suggestCalls++
	f.lastSeed = seed
	f.lastOpts = opts
	return f.suggestResult, f.suggestErr
}

func (f *fakeSuggestClient) Expand(ctx context.Context, seed, letters string, opts googlesuggest.Options, maxRequests int) ([]googlesuggest.Suggestion, error) {
	f.expandCalls++
	f.lastSeed = seed
	f.lastMaxRequests = maxRequests
	f.lastOpts = opts
	return f.expandResult, f.expandErr
}

func runGetSearchSuggestions(t *testing.T, raw string, scope Scope) Result {
	t.Helper()
	result, err := executeGetSearchSuggestions(context.Background(), json.RawMessage(raw), scope)
	if err != nil {
		t.Fatalf("executeGetSearchSuggestions(%s) error = %v", raw, err)
	}
	return result
}

func TestGetSearchSuggestionsUnavailableWithoutClient(t *testing.T) {
	result := runGetSearchSuggestions(t, `{"seed":"seo audit"}`, Scope{})
	if result.Content != getSearchSuggestionsNotAvailable || result.Summary == "" {
		t.Fatalf("result = %+v, want the unavailable state", result)
	}
}

func TestGetSearchSuggestionsArgumentErrors(t *testing.T) {
	fake := &fakeSuggestClient{}
	scope := Scope{Suggest: fake}
	cases := []struct {
		name string
		raw  string
	}{
		{"blank seed", `{"seed":"   "}`},
		{"missing seed", `{}`},
		{"wrong type seed", `{"seed":123}`},
		{"wrong type expand", `{"seed":"seo audit","expand":"yes"}`},
		{"wrong type language", `{"seed":"seo audit","language":5}`},
		{"unknown argument", `{"seed":"seo audit","bogus":true}`},
		{"seed too long", fmt.Sprintf(`{"seed":%q}`, strings.Repeat("a", getSearchSuggestionsMaxSeedLength+1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := executeGetSearchSuggestions(context.Background(), json.RawMessage(tc.raw), scope); err == nil {
				t.Fatalf("executeGetSearchSuggestions(%s) = nil error, want error", tc.raw)
			}
		})
	}
	if fake.suggestCalls != 0 || fake.expandCalls != 0 {
		t.Fatal("a validation error must not reach the client")
	}
}

func TestGetSearchSuggestionsCosts(t *testing.T) {
	t.Run("plain costs one request", func(t *testing.T) {
		fake := &fakeSuggestClient{suggestResult: []googlesuggest.Suggestion{{Phrase: "x"}}}
		budget := NewSuggestBudget(4, suggestRequestsTestLimit)
		runGetSearchSuggestions(t, `{"seed":"seo audit"}`, Scope{Suggest: fake, SuggestBudget: budget})
		if fake.suggestCalls != 1 {
			t.Fatalf("Suggest calls = %d, want 1", fake.suggestCalls)
		}
		if budget.callsLeft != 3 || budget.requestsLeft != suggestRequestsTestLimit-1 {
			t.Fatalf("callsLeft=%d requestsLeft=%d, want 3 and %d", budget.callsLeft, budget.requestsLeft, suggestRequestsTestLimit-1)
		}
	})

	t.Run("expand costs 27 requests", func(t *testing.T) {
		fake := &fakeSuggestClient{expandResult: []googlesuggest.Suggestion{{Phrase: "x"}}}
		budget := NewSuggestBudget(4, suggestRequestsTestLimit)
		runGetSearchSuggestions(t, `{"seed":"seo audit","expand":true}`, Scope{Suggest: fake, SuggestBudget: budget})
		if fake.expandCalls != 1 {
			t.Fatalf("Expand calls = %d, want 1", fake.expandCalls)
		}
		if budget.requestsLeft != suggestRequestsTestLimit-getSearchSuggestionsExpandCost {
			t.Fatalf("requestsLeft = %d, want %d", budget.requestsLeft, suggestRequestsTestLimit-getSearchSuggestionsExpandCost)
		}
		if fake.lastMaxRequests != getSearchSuggestionsExpandCost {
			t.Fatalf("Expand maxRequests = %d, want %d", fake.lastMaxRequests, getSearchSuggestionsExpandCost)
		}
	})
}

func TestGetSearchSuggestionsCallBudget(t *testing.T) {
	fake := &fakeSuggestClient{suggestResult: []googlesuggest.Suggestion{{Phrase: "x"}}}
	budget := NewSuggestBudget(4, suggestRequestsTestLimit)
	scope := Scope{Suggest: fake, SuggestBudget: budget}

	for i := 0; i < 4; i++ {
		if result := runGetSearchSuggestions(t, `{"seed":"seo audit"}`, scope); result.Content == getSearchSuggestionsLimitMessage {
			t.Fatalf("call %d was denied early", i+1)
		}
	}
	result := runGetSearchSuggestions(t, `{"seed":"seo audit"}`, scope)
	if result.Content != getSearchSuggestionsLimitMessage {
		t.Fatalf("fifth call Content = %q, want the limit message", result.Content)
	}
	if fake.suggestCalls != 4 {
		t.Fatalf("client calls = %d, want 4 (no call after exhaustion)", fake.suggestCalls)
	}
}

func TestGetSearchSuggestionsExpandNeedsFullRequestBudget(t *testing.T) {
	fake := &fakeSuggestClient{expandResult: []googlesuggest.Suggestion{{Phrase: "x"}}}
	// One request short of an expand: refuse rather than run a partial expand.
	budget := NewSuggestBudget(4, getSearchSuggestionsExpandCost-1)
	result := runGetSearchSuggestions(t, `{"seed":"seo audit","expand":true}`, Scope{Suggest: fake, SuggestBudget: budget})

	if result.Content != getSearchSuggestionsLimitMessage {
		t.Fatalf("Content = %q, want the limit message", result.Content)
	}
	if fake.expandCalls != 0 {
		t.Fatalf("Expand calls = %d, want 0 (refused before the network)", fake.expandCalls)
	}
	if budget.requestsLeft != getSearchSuggestionsExpandCost-1 {
		t.Fatalf("requestsLeft = %d, want the allowance untouched", budget.requestsLeft)
	}
}

func TestGetSearchSuggestionsPassesHints(t *testing.T) {
	fake := &fakeSuggestClient{suggestResult: []googlesuggest.Suggestion{{Phrase: "x"}}}
	runGetSearchSuggestions(t, `{"seed":"seo audit","language":"en","country":"us"}`, Scope{Suggest: fake})
	if fake.lastSeed != "seo audit" || fake.lastOpts.Language != "en" || fake.lastOpts.Country != "us" {
		t.Fatalf("client args = seed %q opts %+v", fake.lastSeed, fake.lastOpts)
	}
}

func TestGetSearchSuggestionsProviderErrorIsSoft(t *testing.T) {
	fake := &fakeSuggestClient{suggestErr: errors.New("boom: internal endpoint detail")}
	result := runGetSearchSuggestions(t, `{"seed":"seo audit"}`, Scope{Suggest: fake})
	if result.Content != getSearchSuggestionsUnavailable {
		t.Fatalf("Content = %q, want the unavailable message", result.Content)
	}
	if strings.Contains(result.Content, "boom") {
		t.Fatal("Content leaked the underlying provider error")
	}
}

func TestGetSearchSuggestionsFormatting(t *testing.T) {
	suggestions := []googlesuggest.Suggestion{
		{Phrase: "seo audit"},
		{Phrase: "SEO Audit"}, // duplicate after case folding
	}
	for i := 0; i < 70; i++ {
		suggestions = append(suggestions, googlesuggest.Suggestion{Phrase: fmt.Sprintf("phrase %d", i)})
	}
	fake := &fakeSuggestClient{suggestResult: suggestions}
	result := runGetSearchSuggestions(t, `{"seed":"seo audit"}`, Scope{Suggest: fake})

	if !strings.HasPrefix(result.Content, "1. seo audit\n") {
		t.Fatalf("Content = %q, want the numbered list to start with the first spelling", result.Content)
	}
	if strings.Contains(result.Content, "SEO Audit") {
		t.Fatal("Content kept a case-insensitive duplicate")
	}
	if !strings.Contains(result.Content, "11 more suggestions omitted.") {
		t.Fatalf("Content = %q, want 11 omitted", result.Content)
	}
	if want := `Suggestions for "seo audit" · 71 phrases`; result.Summary != want {
		t.Fatalf("Summary = %q, want %q", result.Summary, want)
	}
}

func TestGetSearchSuggestionsTruncatesLongPhrase(t *testing.T) {
	fake := &fakeSuggestClient{suggestResult: []googlesuggest.Suggestion{{Phrase: strings.Repeat("a", getSearchSuggestionsMaxPhraseLength+5)}}}
	result := runGetSearchSuggestions(t, `{"seed":"seo audit"}`, Scope{Suggest: fake})
	want := strings.Repeat("a", getSearchSuggestionsMaxPhraseLength) + "\u2026"
	if !strings.Contains(result.Content, want) {
		t.Fatalf("Content = %q, want the phrase truncated with an ellipsis", result.Content)
	}
}

func TestGetSearchSuggestionsSchemaValid(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(getSearchSuggestionsSchema), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "seed" {
		t.Fatalf("required = %v, want [seed]", schema["required"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", schema["properties"])
	}
	expand, ok := properties["expand"].(map[string]any)
	if !ok || expand["type"] != "boolean" {
		t.Fatalf("expand property = %v, want an optional boolean", properties["expand"])
	}
	for _, name := range required {
		if name == "expand" {
			t.Fatal("expand must stay optional")
		}
	}
}

func TestGetSearchSuggestionsInRegistryAndCatalog(t *testing.T) {
	if _, ok := NewRegistry().Get(getSearchSuggestionsName); !ok {
		t.Fatal("tool missing from the registry")
	}
	found := false
	for _, def := range CatalogDefs() {
		if def.Name == getSearchSuggestionsName {
			found = true
		}
	}
	if !found {
		t.Fatal("tool missing from the catalog")
	}
}
