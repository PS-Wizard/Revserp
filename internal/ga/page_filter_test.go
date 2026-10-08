package ga

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

func stubDataServer(captured *[][]byte, respond func(request map[string]any) any) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*captured = append(*captured, body)
		mu.Unlock()
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		response, _ := json.Marshal(respond(request))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	}))
}

func emptyBatchReports(request map[string]any) any {
	count := 0
	if requests, ok := request["requests"].([]any); ok {
		count = len(requests)
	}
	reports := make([]any, 0, count)
	for range count {
		reports = append(reports, map[string]any{"rows": []any{}})
	}
	return map[string]any{"reports": reports}
}

func requestDimensionFilters(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var batch struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("batch request is not JSON: %v", err)
	}
	if len(batch.Requests) == 0 {
		t.Fatal("batch request carries no sub-requests")
	}
	filters := make([]map[string]any, 0, len(batch.Requests))
	for _, request := range batch.Requests {
		raw, present := request["dimensionFilter"]
		if !present {
			filters = append(filters, nil)
			continue
		}
		filter, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("dimensionFilter is not an object: %v", raw)
		}
		filters = append(filters, filter)
	}
	return filters
}

func TestFetchOverviewWithPagePathFilterPostsPagePathFilters(t *testing.T) {
	var captured [][]byte
	server := stubDataServer(&captured, emptyBatchReports)
	defer server.Close()
	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL

	filter := PagePathFilter{Expression: `^/blog(/.*)?`}
	if _, err := service.FetchOverviewWithPagePathFilter(context.Background(), "token", "123", filter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("expected two batch round trips, got %d", len(captured))
	}
	for _, body := range captured {
		for _, filterValue := range requestDimensionFilters(t, body) {
			if filterValue == nil {
				t.Fatal("scoped overview sub-request lacks dimensionFilter")
			}
			inner, ok := filterValue["filter"].(map[string]any)
			if !ok || inner["fieldName"] != "pagePath" {
				t.Fatalf("filter must target pagePath: %v", filterValue)
			}
			stringFilter, ok := inner["stringFilter"].(map[string]any)
			if !ok || stringFilter["matchType"] != "FULL_REGEXP" || stringFilter["value"] != `^/blog(/.*)?` {
				t.Fatalf("filter must be a FULL_REGEXP pagePath match: %v", filterValue)
			}
		}
	}
}

func TestFetchOverviewWithoutFilterPostsNoDimensionFilters(t *testing.T) {
	var captured [][]byte
	server := stubDataServer(&captured, emptyBatchReports)
	defer server.Close()
	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL

	if _, err := service.FetchOverview(context.Background(), "token", "123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, body := range captured {
		for _, filterValue := range requestDimensionFilters(t, body) {
			if filterValue != nil {
				t.Fatalf("whole-property overview must not send dimensionFilter: %v", filterValue)
			}
		}
	}
}

func TestFetchRealtimePostsMetricsOnly(t *testing.T) {
	var captured [][]byte
	server := stubDataServer(&captured, func(request map[string]any) any {
		return map[string]any{"rows": []any{map[string]any{"metricValues": []any{map[string]any{"value": "7"}}}}}
	})
	defer server.Close()
	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL

	activeUsers, err := service.FetchRealtime(context.Background(), "token", "123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if activeUsers != 7 {
		t.Fatalf("activeUsers = %v, want 7", activeUsers)
	}
	if len(captured) != 1 {
		t.Fatalf("expected one realtime request, got %d", len(captured))
	}
	var request map[string]any
	if err := json.Unmarshal(captured[0], &request); err != nil {
		t.Fatal(err)
	}
	if _, present := request["dimensions"]; present {
		t.Fatalf("property-wide realtime must not invent dimensions: %v", request)
	}
	if _, present := request["dimensionFilter"]; present {
		t.Fatalf("property-wide realtime must not invent a dimensionFilter: %v", request)
	}
}

func TestLocationOverviewCacheKeySeparatesConnectionAndFilter(t *testing.T) {
	filtered := PagePathFilter{Expression: `^/blog(/.*)?`}
	if locationOverviewCacheKey("org", "conn-a", "1", filtered) == locationOverviewCacheKey("org", "conn-a", "1", PagePathFilter{}) {
		t.Error("filter must change the location overview key")
	}
	if locationOverviewCacheKey("org", "conn-a", "1", filtered) == locationOverviewCacheKey("org", "conn-b", "1", filtered) {
		t.Error("connection must change the location overview key")
	}
	if locationOverviewCacheKey("org", "conn-a", "1", filtered) != locationOverviewCacheKey("org", "conn-a", "1", PagePathFilter{Expression: `^/blog(/.*)?`}) {
		t.Error("identical location requests must share a key")
	}
}

func TestPeekLocationOverviewMissesWithoutHTTP(t *testing.T) {
	service := NewService(0)
	if _, ok := service.PeekOverviewCachedWithPagePathFilter("org", "conn", "1", PagePathFilter{Expression: `^/blog(/.*)?`}); ok {
		t.Fatal("peek must miss on an empty cache")
	}
}

func TestInactivePagePathFilter(t *testing.T) {
	if (PagePathFilter{}).active() {
		t.Error("zero filter must be inactive")
	}
	active := PagePathFilter{Expression: `^/blog$`}
	if !active.active() {
		t.Error("expression filter must be active")
	}
	raw, _ := json.Marshal(active.dimensionFilter())
	if !strings.Contains(string(raw), "pagePath") || !strings.Contains(string(raw), "FULL_REGEXP") {
		t.Fatalf("dimensionFilter must target pagePath with FULL_REGEXP: %s", raw)
	}
}
