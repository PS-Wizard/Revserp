package gsc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubDateServer serves date-dimension rows built by buildRows and counts
// upstream requests. No connectivity beyond localhost is needed.
func stubDateServer(t *testing.T, requests *atomic.Int64, buildRows func(start, end string) []map[string]any) *Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		start, _ := payload["startDate"].(string)
		end, _ := payload["endDate"].(string)
		dims, _ := payload["dimensions"].([]any)
		if len(dims) != 1 || dims[0] != "date" {
			t.Errorf("dimensions = %v, want [date]", payload["dimensions"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": buildRows(start, end)})
	}))
	t.Cleanup(server.Close)
	service := NewService("id", "secret", "http://localhost/callback", "secret", 0)
	service.httpClient = server.Client()
	service.searchAnalyticsBaseURL = server.URL
	return service
}

func dateRangeLen(t *testing.T, start, end string) int {
	t.Helper()
	s, err := time.Parse(time.DateOnly, start)
	if err != nil {
		t.Fatalf("bad start %q: %v", start, err)
	}
	e, err := time.Parse(time.DateOnly, end)
	if err != nil {
		t.Fatalf("bad end %q: %v", end, err)
	}
	return int(e.Sub(s).Hours()/24) + 1
}

// TestFetchSummaryHonorsDays pins the bug: days:28 must produce a 28-day
// current window with a 28-day previous window, not the fixed 180-day dates.
func TestFetchSummaryHonorsDays(t *testing.T) {
	var requests atomic.Int64
	service := stubDateServer(t, &requests, func(start, end string) []map[string]any {
		s, _ := time.Parse(time.DateOnly, start)
		e, _ := time.Parse(time.DateOnly, end)
		rows := []map[string]any{}
		for d := s; !d.After(e); d = d.AddDate(0, 0, 1) {
			rows = append(rows, map[string]any{
				"keys": []string{d.Format(time.DateOnly)}, "clicks": 1.0, "impressions": 10.0, "ctr": 0.1, "position": 5.0,
			})
		}
		return rows
	})

	summary28, err := service.FetchSummary(context.Background(), "token", "https://example.com/", 28)
	if err != nil {
		t.Fatalf("FetchSummary(28): %v", err)
	}
	if summary28.Days != 28 {
		t.Fatalf("Days = %d, want 28", summary28.Days)
	}
	if got := dateRangeLen(t, summary28.Range.CurrentStart, summary28.Range.CurrentEnd); got != 28 {
		t.Fatalf("current window = %d days (%s..%s), want 28", got, summary28.Range.CurrentStart, summary28.Range.CurrentEnd)
	}
	if got := dateRangeLen(t, summary28.Range.PreviousStart, summary28.Range.PreviousEnd); got != 28 {
		t.Fatalf("previous window = %d days (%s..%s), want 28", got, summary28.Range.PreviousStart, summary28.Range.PreviousEnd)
	}
	// Previous window ends the day before the current window starts.
	curStart, _ := time.Parse(time.DateOnly, summary28.Range.CurrentStart)
	prevEnd, _ := time.Parse(time.DateOnly, summary28.Range.PreviousEnd)
	if prevEnd.AddDate(0, 0, 1) != curStart {
		t.Fatalf("previous end %s is not the day before current start %s", summary28.Range.PreviousEnd, summary28.Range.CurrentStart)
	}
	// Same 3-day lag as row reports: current end is 3 days before today.
	wantEnd := time.Now().UTC().AddDate(0, 0, -3).Format(time.DateOnly)
	if summary28.Range.CurrentEnd != wantEnd {
		t.Fatalf("CurrentEnd = %s, want %s (3-day lag)", summary28.Range.CurrentEnd, wantEnd)
	}
	if summary28.Summary.Clicks.Current != 28 || summary28.Summary.Clicks.Previous != 28 {
		t.Fatalf("clicks = %+v, want 28/28", summary28.Summary.Clicks)
	}
	if summary28.Summary.Impressions.Current != 280 || summary28.Summary.Impressions.Previous != 280 {
		t.Fatalf("impressions = %+v, want 280/280", summary28.Summary.Impressions)
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d, want 1 (current+previous in one fetch)", requests.Load())
	}

	// days:180 must produce a different, 180-day window.
	summary180, err := service.FetchSummary(context.Background(), "token", "https://example.com/", 180)
	if err != nil {
		t.Fatalf("FetchSummary(180): %v", err)
	}
	if got := dateRangeLen(t, summary180.Range.CurrentStart, summary180.Range.CurrentEnd); got != 180 {
		t.Fatalf("180-day current window = %d days, want 180", got)
	}
	if summary180.Range.CurrentStart == summary28.Range.CurrentStart {
		t.Fatalf("days:28 and days:180 share current start %s", summary28.Range.CurrentStart)
	}
	if summary180.Summary.Clicks.Current != 180 {
		t.Fatalf("180-day clicks = %v, want 180", summary180.Summary.Clicks.Current)
	}
}

// TestFetchSummaryCachedKeysByDaysAndOrg verifies the cache identity: days is
// part of the key and organizationID isolates tenants.
func TestFetchSummaryCachedKeysByDaysAndOrg(t *testing.T) {
	var requests atomic.Int64
	service := stubDateServer(t, &requests, func(start, end string) []map[string]any {
		return []map[string]any{{"keys": []string{start}, "clicks": 1.0, "impressions": 10.0, "ctr": 0.1, "position": 5.0}}
	})
	ctx := context.Background()

	if _, err := service.FetchSummaryCached(ctx, "tok", "org-a", "https://example.com/", 28); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FetchSummaryCached(ctx, "tok", "org-a", "https://example.com/", 28); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("repeat same days/org = %d requests, want 1 (cached)", got)
	}
	if _, err := service.FetchSummaryCached(ctx, "tok", "org-a", "https://example.com/", 180); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("different days = %d requests, want 2", got)
	}
	if _, err := service.FetchSummaryCached(ctx, "tok", "org-b", "https://example.com/", 28); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("different org = %d requests, want 3 (no cross-org cache)", got)
	}
}

// TestFetchSummaryClampsDays keeps the arbitrary 7..480 contract.
func TestFetchSummaryClampsDays(t *testing.T) {
	var requests atomic.Int64
	service := stubDateServer(t, &requests, func(start, end string) []map[string]any { return nil })
	summary, err := service.FetchSummary(context.Background(), "token", "https://example.com/", 9999)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Days != queryPageMaxDays {
		t.Fatalf("Days = %d, want %d", summary.Days, queryPageMaxDays)
	}
	if !strings.Contains(summary.Range.CurrentStart, "-") {
		t.Fatalf("bad range start %q", summary.Range.CurrentStart)
	}
}
