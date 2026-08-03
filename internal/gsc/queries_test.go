package gsc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

// captureQueryRequest runs FetchQueries against a stub Google and returns the
// request body the service sent.
func captureQueryRequest(t *testing.T, options QueryPageOptions, rowCount int) (map[string]any, QueryPage) {
	t.Helper()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}

		rows := make([]map[string]any, 0, rowCount)
		for i := range rowCount {
			rows = append(rows, map[string]any{
				"keys":        []string{"query " + string(rune('a'+i%26))},
				"clicks":      float64(i),
				"impressions": float64(i * 10),
				"ctr":         0.1,
				"position":    5.0,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"rows": rows})
	}))
	t.Cleanup(server.Close)

	service := NewService("id", "secret", "http://localhost/callback", "secret", 0)
	service.httpClient = server.Client()
	service.searchAnalyticsBaseURL = server.URL

	page, err := service.FetchQueries(context.Background(), "token", "https://example.com/", options)
	if err != nil {
		t.Fatalf("FetchQueries: %v", err)
	}
	return captured, page
}

func TestFetchQueriesRequestsOneRowPastThePage(t *testing.T) {
	captured, page := captureQueryRequest(t, QueryPageOptions{Limit: 10, Offset: 40}, 11)

	if got := captured["rowLimit"]; got != float64(11) {
		t.Errorf("rowLimit = %v, want 11", got)
	}
	if got := captured["startRow"]; got != float64(40) {
		t.Errorf("startRow = %v, want 40", got)
	}
	if len(page.Rows) != 10 {
		t.Errorf("returned %d rows, want the extra row trimmed to 10", len(page.Rows))
	}
	if !page.HasMore {
		t.Error("HasMore = false, want true when Google returned the extra row")
	}
}

func TestFetchQueriesReportsLastPage(t *testing.T) {
	_, page := captureQueryRequest(t, QueryPageOptions{Limit: 10}, 4)

	if page.HasMore {
		t.Error("HasMore = true, want false when Google returned fewer rows than the page size")
	}
	if len(page.Rows) != 4 {
		t.Errorf("returned %d rows, want 4", len(page.Rows))
	}
}

func TestFetchQueriesSendsNoFilterGroupWhenUnfiltered(t *testing.T) {
	captured, _ := captureQueryRequest(t, QueryPageOptions{Limit: 5}, 1)

	if _, ok := captured["dimensionFilterGroups"]; ok {
		t.Error("dimensionFilterGroups sent for an unfiltered request")
	}
}

func TestFetchQueriesPushesQuestionAndSearchFiltersToGoogle(t *testing.T) {
	captured, _ := captureQueryRequest(t, QueryPageOptions{Limit: 5, Search: "seo tool", QuestionsOnly: true}, 1)

	groups, ok := captured["dimensionFilterGroups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("dimensionFilterGroups = %v, want one group", captured["dimensionFilterGroups"])
	}
	group := groups[0].(map[string]any)
	if group["groupType"] != "and" {
		t.Errorf("groupType = %v, want and", group["groupType"])
	}

	filters := group["filters"].([]any)
	if len(filters) != 2 {
		t.Fatalf("got %d filters, want 2", len(filters))
	}

	question := filters[0].(map[string]any)
	if question["operator"] != "includingRegex" || question["expression"] != questionQueryPattern {
		t.Errorf("question filter = %v, want includingRegex with the question pattern", question)
	}

	search := filters[1].(map[string]any)
	if search["operator"] != "contains" || search["expression"] != "seo tool" {
		t.Errorf("search filter = %v, want contains \"seo tool\"", search)
	}
}

func TestQueryPageOptionsNormalizedClampsBounds(t *testing.T) {
	tests := []struct {
		name  string
		given QueryPageOptions
		want  QueryPageOptions
	}{
		{"defaults", QueryPageOptions{}, QueryPageOptions{Days: queryPageDefaultDays, Limit: queryPageDefaultLimit}},
		{"over max", QueryPageOptions{Days: 9999, Limit: 9999, Offset: 999999}, QueryPageOptions{Days: queryPageMaxDays, Limit: queryPageMaxLimit, Offset: queryPageMaxOffset}},
		{"under min", QueryPageOptions{Days: 1, Limit: 1, Offset: -5}, QueryPageOptions{Days: queryPageMinDays, Limit: 1}},
		{"trims search", QueryPageOptions{Search: "  seo  "}, QueryPageOptions{Days: queryPageDefaultDays, Limit: queryPageDefaultLimit, Search: "seo"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.given.normalized(); got != test.want {
				t.Errorf("normalized() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestQueryPageOptionsNormalizedCapsSearchLength(t *testing.T) {
	long := make([]byte, queryPageMaxSearch+50)
	for i := range long {
		long[i] = 'a'
	}

	got := QueryPageOptions{Search: string(long)}.normalized()
	if len(got.Search) != queryPageMaxSearch {
		t.Errorf("search length = %d, want %d", len(got.Search), queryPageMaxSearch)
	}
}

func TestCacheKeyDistinguishesEveryRequestDimension(t *testing.T) {
	base := QueryPageOptions{Days: 90, Limit: 25, Offset: 0, Search: "seo"}
	variants := map[string]QueryPageOptions{
		"days":      {Days: 30, Limit: 25, Search: "seo"},
		"limit":     {Days: 90, Limit: 50, Search: "seo"},
		"offset":    {Days: 90, Limit: 25, Offset: 25, Search: "seo"},
		"search":    {Days: 90, Limit: 25, Search: "aeo"},
		"questions": {Days: 90, Limit: 25, Search: "seo", QuestionsOnly: true},
	}

	baseKey := base.cacheKey("org", "site")
	for name, variant := range variants {
		if variant.cacheKey("org", "site") == baseKey {
			t.Errorf("%s does not change the cache key", name)
		}
	}
	if base.cacheKey("org-a", "site") == base.cacheKey("org-b", "site") {
		t.Error("organization does not change the cache key")
	}
}

// TestQuestionQueryPatternMatchesRealQueries pins the pattern's behavior. It is
// compiled with Go's regexp, which is the same RE2 engine Google's
// includingRegex uses.
func TestQuestionQueryPatternMatchesRealQueries(t *testing.T) {
	pattern := regexp.MustCompile(questionQueryPattern)

	matches := []string{
		"what is the best seo tool",
		"who is the best seo tool",
		"Why Does My Site Rank Low",
		"how to fix meta descriptions",
		"which seo tool is best",
		"ahrefs vs semrush",
		"semrush versus moz",
		"best seo tool 2026",
		"is seo worth it",
		"should i use schema markup",
	}
	for _, query := range matches {
		if !pattern.MatchString(query) {
			t.Errorf("pattern did not match question query %q", query)
		}
	}

	nonMatches := []string{
		"seo tool",
		"revketer pricing",
		"technical seo audit checklist",
		"whatsapp marketing",
	}
	for _, query := range nonMatches {
		if pattern.MatchString(query) {
			t.Errorf("pattern matched non-question query %q", query)
		}
	}
}
