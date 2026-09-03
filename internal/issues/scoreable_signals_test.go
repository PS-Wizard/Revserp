package issues

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// TestIsScoreablePageExcludesSoft404AndFetchErrors verifies that soft-404 pages
// (status 200) and fetch-failed pages are excluded from the coverage denominator.
func TestIsScoreablePageExcludesSoft404AndFetchErrors(t *testing.T) {
	tests := []struct {
		name  string
		page  shared.CrawlPageSignal
		score bool
	}{
		{
			name:  "healthy html page is scoreable",
			page:  shared.CrawlPageSignal{URL: "https://a.test/", StatusCode: 200, ContentType: "text/html"},
			score: true,
		},
		{
			name:  "soft 404 with status 200 is excluded",
			page:  shared.CrawlPageSignal{URL: "https://a.test/soft", StatusCode: 200, ContentType: "text/html", Soft404: true},
			score: false,
		},
		{
			name:  "fetch error is excluded",
			page:  shared.CrawlPageSignal{URL: "https://a.test/err", StatusCode: 200, ContentType: "text/html", FetchError: "timeout"},
			score: false,
		},
		{
			name:  "status 404 is excluded",
			page:  shared.CrawlPageSignal{URL: "https://a.test/404", StatusCode: 404, ContentType: "text/html"},
			score: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shared.IsScoreablePage(tt.page); got != tt.score {
				t.Fatalf("IsScoreablePage(%+v) = %v, want %v", tt.page, got, tt.score)
			}
		})
	}
}

// TestPageSignalsFromRowsCarriesSoft404AndFetchError verifies the potential-path
// signal mapping propagates soft_404/fetch_error from the (now wider) query row.
func TestPageSignalsFromRowsCarriesSoft404AndFetchError(t *testing.T) {
	rows := []sqlc.ListCrawlPageSignalsForCrawlRow{
		{Url: "https://a.test/soft", StatusCode: pgtype.Int4{Int32: 200, Valid: true}, ContentType: pgtype.Text{String: "text/html", Valid: true}, Soft404: true},
		{Url: "https://a.test/err", StatusCode: pgtype.Int4{Int32: 200, Valid: true}, ContentType: pgtype.Text{String: "text/html", Valid: true}, FetchError: pgtype.Text{String: "dns failure", Valid: true}},
		{Url: "https://a.test/ok", StatusCode: pgtype.Int4{Int32: 200, Valid: true}, ContentType: pgtype.Text{String: "text/html", Valid: true}},
	}

	signals := PageSignalsFromRows(rows)
	if len(signals) != len(rows) {
		t.Fatalf("got %d signals, want %d", len(signals), len(rows))
	}
	if !signals[0].Soft404 {
		t.Error("soft-404 row lost Soft404=true in mapping")
	}
	if signals[1].FetchError == "" {
		t.Error("fetch-error row lost FetchError in mapping")
	}
	for _, signal := range signals[:2] {
		if shared.IsScoreablePage(signal) {
			t.Errorf("signal for %s should not be scoreable", signal.URL)
		}
	}
	if !shared.IsScoreablePage(signals[2]) {
		t.Error("healthy signal should be scoreable")
	}
}
