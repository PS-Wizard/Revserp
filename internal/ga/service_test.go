package ga

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestListPropertiesPaginates(t *testing.T) {
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.URL.Query().Get("pageToken"))
		if r.URL.Query().Get("pageToken") == "next" {
			_ = json.NewEncoder(w).Encode(map[string]any{"accountSummaries": []any{map[string]any{"displayName": "Account B", "propertySummaries": []any{map[string]any{"property": "properties/2", "displayName": "Two"}}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accountSummaries": []any{map[string]any{"displayName": "Account A", "propertySummaries": []any{map[string]any{"property": "properties/1", "displayName": "One"}}}}, "nextPageToken": "next"})
	}))
	defer server.Close()

	service := NewService(0)
	service.httpClient, service.adminBaseURL = server.Client(), server.URL
	properties, err := service.ListProperties(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(properties) != 2 || properties[0].PropertyID != "1" || properties[1].AccountDisplayName != "Account B" {
		t.Fatalf("properties = %#v", properties)
	}
	if got := strings.Join(tokens, ","); got != ",next" {
		t.Fatalf("page tokens = %q", got)
	}
}

func TestReportErrorsAndResponseLimit(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		limit int64
		want  string
	}{
		{"non-2xx", `{"error":{"message":"denied"}}`, 0, "denied"},
		{"limit", strings.Repeat("x", 20), 10, "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			service := NewService(test.limit)
			service.httpClient, service.dataBaseURL = server.Client(), server.URL
			_, err := service.FetchRealtime(context.Background(), "token", "123")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if test.name == "non-2xx" {
				var apiErr *Error
				if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
					t.Fatalf("err = %#v, want typed forbidden error", err)
				}
			}
		})
	}
}

func TestFetchOverviewUsesTwoBatchRequests(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []map[string]any `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		reports := make([]map[string]any, len(body.Requests))
		for index := range reports {
			reports[index] = map[string]any{"rows": []any{}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reports": reports})
	}))
	defer server.Close()

	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL
	if _, err := service.FetchOverview(context.Background(), "token", "123"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("http requests = %d, want 2", len(paths))
	}
	for _, path := range paths {
		if !strings.HasSuffix(path, ":batchRunReports") {
			t.Fatalf("path = %q, want the :batchRunReports endpoint", path)
		}
	}
}

func TestFetchOverviewBatchSplitAndOrder(t *testing.T) {
	var mu sync.Mutex
	var batches [][]map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []map[string]any `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		batches = append(batches, body.Requests)
		mu.Unlock()
		reports := make([]map[string]any, len(body.Requests))
		for index := range reports {
			reports[index] = map[string]any{"rows": []any{}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reports": reports})
	}))
	defer server.Close()

	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL
	if _, err := service.FetchOverview(context.Background(), "token", "123"); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("http requests = %d, want 2", len(batches))
	}
	var batchA, batchB []map[string]any
	for _, batch := range batches {
		if len(batch) > 5 {
			t.Fatalf("batch has %d requests, want at most 5", len(batch))
		}
		switch len(batch) {
		case 5:
			batchA = batch
		case 3:
			batchB = batch
		}
	}
	if batchA == nil || batchB == nil {
		t.Fatalf("batch sizes = %d and %d, want a 5-request and a 3-request batch", len(batches[0]), len(batches[1]))
	}

	wantA := []string{"", "", "date", "landingPagePlusQueryString", "sessionDefaultChannelGroup"}
	wantB := []string{"sessionSourceMedium", "country", "deviceCategory"}
	for index, want := range wantA {
		if got := requestDimension(batchA[index]); got != want {
			t.Fatalf("batch A request %d dimension = %q, want %q", index, got, want)
		}
	}
	for index, want := range wantB {
		if got := requestDimension(batchB[index]); got != want {
			t.Fatalf("batch B request %d dimension = %q, want %q", index, got, want)
		}
	}
	if batchA[3]["limit"] != "50" || batchA[4]["limit"] != "25" || batchB[0]["limit"] != "50" || batchB[1]["limit"] != "25" || batchB[2]["limit"] != "25" {
		t.Fatalf("breakdown limits = %v %v %v %v %v", batchA[3]["limit"], batchA[4]["limit"], batchB[0]["limit"], batchB[1]["limit"], batchB[2]["limit"])
	}
}

func TestFetchOverviewParsesBatchResponses(t *testing.T) {
	currentStart := time.Now().UTC().AddDate(0, 0, -180).Format(time.DateOnly)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []map[string]any `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		reports := make([]map[string]any, 0, len(body.Requests))
		for _, request := range body.Requests {
			switch dimension := requestDimension(request); {
			case dimension == "date":
				reports = append(reports, reportBody("20260102", "2", "3", "0.5", "4"))
			case dimension != "":
				reports = append(reports, reportBody(dimension, "1", "2", "0.25", "3"))
			case requestStartDate(request) == currentStart:
				reports = append(reports, reportBody("", "10", "0", "0", "0"))
			default:
				reports = append(reports, reportBody("", "9", "0", "0", "0"))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reports": reports})
	}))
	defer server.Close()

	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL
	overview, err := service.FetchOverview(context.Background(), "token", "123")
	if err != nil {
		t.Fatal(err)
	}
	if trend := overview.Trend; len(trend) != 1 || trend[0].Date != "2026-01-02" || trend[0].ActiveUsers != 2 || trend[0].Sessions != 3 || trend[0].EngagementRate != 0.5 || trend[0].KeyEvents != 4 {
		t.Fatalf("trend = %#v", trend)
	}
	if overview.Summary.ActiveUsers != (MetricPair{Current: 10, Previous: 9}) {
		t.Fatalf("summary active users = %#v", overview.Summary.ActiveUsers)
	}
	for _, breakdown := range []struct {
		name  string
		rows  []BreakdownRow
		label string
	}{
		{"landing pages", overview.LandingPages, "landingPagePlusQueryString"},
		{"channels", overview.Channels, "sessionDefaultChannelGroup"},
		{"sources", overview.Sources, "sessionSourceMedium"},
		{"countries", overview.Countries, "country"},
		{"devices", overview.Devices, "deviceCategory"},
	} {
		if len(breakdown.rows) != 1 {
			t.Fatalf("%s rows = %#v", breakdown.name, breakdown.rows)
		}
		row := breakdown.rows[0]
		if row.Label != breakdown.label || row.ActiveUsers != 1 || row.Sessions != 2 || row.EngagementRate != 0.25 || row.KeyEvents != 3 {
			t.Fatalf("%s row = %#v, want label %q", breakdown.name, row, breakdown.label)
		}
	}
}

func TestFetchOverviewFailsOnBatchSubReportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []map[string]any `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		reports := make([]map[string]any, 0, len(body.Requests))
		for _, request := range body.Requests {
			if requestDimension(request) == "date" {
				reports = append(reports, map[string]any{"error": map[string]any{"message": "sub report boom"}})
				continue
			}
			reports = append(reports, reportBody("", "1", "1", "1", "1"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reports": reports})
	}))
	defer server.Close()

	service := NewService(0)
	service.httpClient, service.dataBaseURL = server.Client(), server.URL
	_, err := service.FetchOverview(context.Background(), "token", "123")
	if err == nil || !strings.Contains(err.Error(), "sub report boom") {
		t.Fatalf("err = %v, want the failing sub-report error", err)
	}
}

func requestDimension(request map[string]any) string {
	dimensions, ok := request["dimensions"].([]any)
	if !ok || len(dimensions) == 0 {
		return ""
	}
	name, _ := dimensions[0].(map[string]any)["name"].(string)
	return name
}

func requestStartDate(request map[string]any) string {
	ranges, ok := request["dateRanges"].([]any)
	if !ok || len(ranges) == 0 {
		return ""
	}
	start, _ := ranges[0].(map[string]any)["startDate"].(string)
	return start
}

func reportBody(dimensionValue string, metrics ...string) map[string]any {
	metricValues := make([]any, 0, len(metrics))
	for _, metric := range metrics {
		metricValues = append(metricValues, map[string]any{"value": metric})
	}
	row := map[string]any{"metricValues": metricValues}
	if dimensionValue != "" {
		row["dimensionValues"] = []any{map[string]any{"value": dimensionValue}}
	}
	return map[string]any{"rows": []any{row}}
}
