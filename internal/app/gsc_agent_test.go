package app

import (
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/app/aitools"
	"github.com/ps-wizard/revserp/internal/gsc"
)

func testOverviewWindow() gsc.OverviewWindow {
	return gsc.OverviewWindow{
		Range: gsc.OverviewRange{
			CurrentStart:  "2026-01-01",
			CurrentEnd:    "2026-06-29",
			PreviousStart: "2025-07-05",
			PreviousEnd:   "2025-12-31",
		},
		Summary: gsc.OverviewSummary{
			Clicks:      gsc.MetricSummary{Current: 500, Previous: 400},
			Impressions: gsc.MetricSummary{Current: 10000, Previous: 9000},
		},
		TopPages: []gsc.SearchAnalyticsRow{
			{Page: "https://example.com/", Clicks: 120, Impressions: 3000},
			{Page: "https://example.com/about", Clicks: 30, Impressions: 900},
		},
		CountryBreakdown: []gsc.SearchAnalyticsRow{
			{Country: "usa", Clicks: 300, Impressions: 6000},
			{Country: "ind", Clicks: 120, Impressions: 2500},
			{Country: "gbr", Clicks: 80, Impressions: 1500},
		},
		DeviceBreakdown: []gsc.SearchAnalyticsRow{
			{Device: "MOBILE", Clicks: 280, Impressions: 6200},
			{Device: "DESKTOP", Clicks: 200, Impressions: 3400},
			{Device: "TABLET", Clicks: 20, Impressions: 400},
		},
		Opportunities: gsc.OverviewOpportunities{
			LowCTRQueries:           []gsc.SearchAnalyticsRow{{Query: "revtube", Impressions: 722, Position: 3.6, CTR: 0.005}},
			StrikingDistanceQueries: []gsc.SearchAnalyticsRow{{Query: "seo audit tool", Impressions: 400, Position: 8.2}},
		},
	}
}

// Every window-backed report must produce content and state the window it
// covers, so the model can never present the wrong date range as fact.
func TestMarshalSearchConsoleWindowReportCoversEveryWindowReport(t *testing.T) {
	window := testOverviewWindow()

	tests := []struct {
		report   string
		wantKeys []string
	}{
		{"summary", []string{"site_url", "range", "summary"}},
		{"top_pages", []string{"site_url", "start_date", "end_date", "rows"}},
		{"countries", []string{"site_url", "start_date", "end_date", "rows"}},
		{"devices", []string{"site_url", "start_date", "end_date", "rows"}},
		{"opportunities", []string{"site_url", "start_date", "end_date", "low_ctr_queries", "striking_distance_queries"}},
	}

	for _, test := range tests {
		t.Run(test.report, func(t *testing.T) {
			report, err := marshalSearchConsoleWindowReport(
				"https://example.com/",
				window,
				aitools.SearchConsoleQuery{Report: test.report, Limit: 25},
			)
			if err != nil {
				t.Fatalf("marshalSearchConsoleWindowReport: %v", err)
			}
			if report.Unavailable != "" {
				t.Errorf("Unavailable = %q, want empty", report.Unavailable)
			}
			if report.Summary == "" {
				t.Error("Summary is empty")
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(report.Content), &payload); err != nil {
				t.Fatalf("content is not valid JSON: %v", err)
			}
			for _, key := range test.wantKeys {
				if _, ok := payload[key]; !ok {
					t.Errorf("payload is missing %q; got keys %v", key, payload)
				}
			}
		})
	}
}

func TestMarshalSearchConsoleWindowReportReturnsCountryAndDeviceRows(t *testing.T) {
	window := testOverviewWindow()

	tests := []struct {
		report   string
		wantRows int
	}{
		{"countries", len(window.CountryBreakdown)},
		{"devices", len(window.DeviceBreakdown)},
	}

	for _, test := range tests {
		t.Run(test.report, func(t *testing.T) {
			report, err := marshalSearchConsoleWindowReport(
				"https://example.com/",
				window,
				aitools.SearchConsoleQuery{Report: test.report, Limit: 25},
			)
			if err != nil {
				t.Fatalf("marshalSearchConsoleWindowReport: %v", err)
			}

			var payload struct {
				Rows []gsc.SearchAnalyticsRow `json:"rows"`
			}
			if err := json.Unmarshal([]byte(report.Content), &payload); err != nil {
				t.Fatalf("unmarshal content: %v", err)
			}
			if len(payload.Rows) != test.wantRows {
				t.Errorf("got %d rows, want %d", len(payload.Rows), test.wantRows)
			}
		})
	}
}

func TestMarshalSearchConsoleWindowReportAppliesLimit(t *testing.T) {
	report, err := marshalSearchConsoleWindowReport(
		"https://example.com/",
		testOverviewWindow(),
		aitools.SearchConsoleQuery{Report: "countries", Limit: 2},
	)
	if err != nil {
		t.Fatalf("marshalSearchConsoleWindowReport: %v", err)
	}

	var payload struct {
		Rows []gsc.SearchAnalyticsRow `json:"rows"`
	}
	if err := json.Unmarshal([]byte(report.Content), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if len(payload.Rows) != 2 {
		t.Errorf("got %d rows, want the limit of 2 applied", len(payload.Rows))
	}
}
