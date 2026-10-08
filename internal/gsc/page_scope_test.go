package gsc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func stubSearchAnalyticsServer(captured *[][]byte) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*captured = append(*captured, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rows":[]}`))
	}))
}

func postedDimensionFilters(t *testing.T, bodies [][]byte) []map[string]any {
	t.Helper()
	if len(bodies) == 0 {
		t.Fatal("no requests reached the stub server")
	}
	var payload map[string]any
	if err := json.Unmarshal(bodies[0], &payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	groups, ok := payload["dimensionFilterGroups"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("payload lacks dimensionFilterGroups: %v", payload)
	}
	group, ok := groups[0].(map[string]any)
	if !ok || group["groupType"] != "and" {
		t.Fatalf("filter group is not an and-group: %v", groups[0])
	}
	rawFilters, ok := group["filters"].([]any)
	if !ok {
		t.Fatalf("filter group lacks filters: %v", group)
	}
	filters := make([]map[string]any, 0, len(rawFilters))
	for _, raw := range rawFilters {
		filter, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("filter is not an object: %v", raw)
		}
		filters = append(filters, filter)
	}
	return filters
}

func TestFetchOverviewWithPageScopeSendsPageFilter(t *testing.T) {
	var captured [][]byte
	server := stubSearchAnalyticsServer(&captured)
	defer server.Close()
	service := NewService("client", "secret", "https://app.example/callback", "encryption-secret", 0)
	service.searchAnalyticsBaseURL = server.URL
	service.httpClient = server.Client()

	scope := PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: `^https://example\.com/blog(/.*)?(\?.*)?$`}
	if _, err := service.FetchOverviewWithPageScope(context.Background(), "token", "https://example.com/", scope); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, body := range captured {
		found := false
		for _, filter := range postedDimensionFilters(t, [][]byte{body}) {
			if filter["dimension"] == "page" && filter["operator"] == PageScopeOperatorIncludingRegex && filter["expression"] == scope.Expression {
				found = true
			}
		}
		if !found {
			t.Fatalf("overview request lacks the page scope filter: %s", body)
		}
	}
}

func TestFetchOverviewWithoutScopeSendsNoFilterGroups(t *testing.T) {
	var captured [][]byte
	server := stubSearchAnalyticsServer(&captured)
	defer server.Close()
	service := NewService("client", "secret", "https://app.example/callback", "encryption-secret", 0)
	service.searchAnalyticsBaseURL = server.URL
	service.httpClient = server.Client()

	if _, err := service.FetchOverview(context.Background(), "token", "https://example.com/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("no requests reached the stub server")
	}
	var payload map[string]any
	if err := json.Unmarshal(captured[0], &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["dimensionFilterGroups"]; present {
		t.Fatalf("whole-property overview must not send filter groups: %v", payload)
	}
}

func TestFetchQueriesWithPageScopeComposesFilters(t *testing.T) {
	var captured [][]byte
	server := stubSearchAnalyticsServer(&captured)
	defer server.Close()
	service := NewService("client", "secret", "https://app.example/callback", "encryption-secret", 0)
	service.searchAnalyticsBaseURL = server.URL
	service.httpClient = server.Client()

	options := QueryPageOptions{Days: 7, Limit: 10, Search: "guide", PageScope: PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: `^https://example\.com/blog/?(\?.*)?$`}}
	if _, err := service.FetchQueries(context.Background(), "token", "https://example.com/", options); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	filters := postedDimensionFilters(t, captured)
	dimensions := map[string]bool{}
	for _, filter := range filters {
		dimensions[filter["dimension"].(string)] = true
	}
	if !dimensions["query"] || !dimensions["page"] {
		t.Fatalf("scoped search must combine query and page filters: %v", filters)
	}
}

func TestLocationCacheKeysSeparateConnectionAndScope(t *testing.T) {
	unscoped := QueryPageOptions{Days: 7, Limit: 10}
	scoped := unscoped
	scoped.PageScope = PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: `^https://example\.com/blog(/.*)?(\?.*)?$`}
	otherScope := unscoped
	otherScope.PageScope = PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: `^https://example\.com/news(/.*)?(\?.*)?$`}

	if unscoped.locationCacheKey("org", "conn-a", "site") == scoped.locationCacheKey("org", "conn-a", "site") {
		t.Error("scope must change the location queries key")
	}
	if scoped.locationCacheKey("org", "conn-a", "site") == otherScope.locationCacheKey("org", "conn-a", "site") {
		t.Error("scope expression must change the location queries key")
	}
	if scoped.locationCacheKey("org", "conn-a", "site") == scoped.locationCacheKey("org", "conn-b", "site") {
		t.Error("connection must change the location queries key")
	}
	if scoped.locationCacheKey("org", "conn-a", "site") != scoped.locationCacheKey("org", "conn-a", "site") {
		t.Error("identical location requests must share a key")
	}
	if locationOverviewCacheKey("org", "conn-a", "site", scoped.PageScope) == locationOverviewCacheKey("org", "conn-a", "site", PageScopeFilter{}) {
		t.Error("scope must change the location overview key")
	}
	if locationOverviewCacheKey("org", "conn-a", "site", scoped.PageScope) == locationOverviewCacheKey("org", "conn-b", "site", scoped.PageScope) {
		t.Error("connection must change the location overview key")
	}
}

func TestPeekLocationCachesMissWithoutHTTP(t *testing.T) {
	service := NewService("client", "secret", "https://app.example/callback", "encryption-secret", 0)
	scope := PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: `^https://example\.com/blog(/.*)?(\?.*)?$`}
	if _, ok := service.PeekOverviewCachedWithPageScope("org", "conn", "site", scope); ok {
		t.Fatal("peek must miss on an empty cache")
	}
	options := QueryPageOptions{Days: 7, Limit: 10, PageScope: scope}
	if _, ok := service.PeekLocationQueriesCached("org", "conn", "site", options); ok {
		t.Fatal("peek must miss on an empty cache")
	}
}

func TestInactivePageScopeSendsNothing(t *testing.T) {
	if (PageScopeFilter{}).active() {
		t.Error("zero filter must be inactive")
	}
	if (PageScopeFilter{Operator: "equals", Expression: "x"}).active() {
		t.Error("equals operator is unsupported and must stay inactive")
	}
	if (PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: "  "}).active() {
		t.Error("blank expression must be inactive")
	}
	if !strings.Contains((PageScopeFilter{Operator: PageScopeOperatorIncludingRegex, Expression: "x"}).cacheKeyPart(), "includingRegex") {
		t.Error("active scope must contribute to the cache key")
	}
}
