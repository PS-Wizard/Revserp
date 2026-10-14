package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The MCP get_crawl snapshot is a requested-crawl poll surface, so it
// carries the same raw progress JSON when the worker wrote it.
func TestMCPCrawlRowCarriesAnalysisProgress(t *testing.T) {
	progress := []byte(`{"stage":"checking_links","links_resolved":640000}`)
	row := mcpCrawlRowFromGet(sqlc.GetCrawlByIDForUserRow{Status: "running", AnalysisProgress: progress})
	if string(row.AnalysisProgress) != string(progress) {
		t.Fatalf("mcp get progress = %s, want the raw persisted JSON", row.AnalysisProgress)
	}
	listed := mcpCrawlRowFromList(sqlc.ListCrawlsForProjectRow{Status: "running", AnalysisProgress: progress})
	if string(listed.AnalysisProgress) != string(progress) {
		t.Fatalf("mcp list progress = %s, want the raw persisted JSON", listed.AnalysisProgress)
	}
	legacy := mcpCrawlRowFromGet(sqlc.GetCrawlByIDForUserRow{Status: "completed"})
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("mcp marshal: %v", err)
	}
	if strings.Contains(string(encoded), "analysis_progress") {
		t.Fatalf("mcp payload = %s, want no analysis_progress key for legacy rows", encoded)
	}
}

// Persisted analysis progress rides the existing crawl snapshots as raw JSON:
// present verbatim when the worker wrote it, absent for legacy rows.
func TestCrawlResponseCarriesAnalysisProgress(t *testing.T) {
	createdAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	progress := []byte(`{"stage":"checking_links","links_resolved":640000}`)

	response := newCrawlResponseFromGetRow(sqlc.GetCrawlByIDForUserRow{
		Status: "running", AnalysisProgress: progress, CreatedAt: createdAt,
	})
	if string(response.AnalysisProgress) != string(progress) {
		t.Fatalf("detail progress = %s, want the raw persisted JSON", response.AnalysisProgress)
	}

	listed := newCrawlResponseFromListRow(sqlc.ListCrawlsForProjectRow{
		Status: "running", AnalysisProgress: progress, CreatedAt: createdAt,
	})
	if string(listed.AnalysisProgress) != string(progress) {
		t.Fatalf("list progress = %s, want the raw persisted JSON", listed.AnalysisProgress)
	}
}

func TestCrawlResponseOmitsMissingAnalysisProgress(t *testing.T) {
	createdAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for name, response := range map[string]crawlResponse{
		"detail": newCrawlResponseFromGetRow(sqlc.GetCrawlByIDForUserRow{Status: "completed", CreatedAt: createdAt}),
		"list":   newCrawlResponseFromListRow(sqlc.ListCrawlsForProjectRow{Status: "completed", CreatedAt: createdAt}),
	} {
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		if strings.Contains(string(encoded), "analysis_progress") {
			t.Fatalf("%s payload = %s, want no analysis_progress key for legacy rows", name, encoded)
		}
	}

	active := activeCrawlResponse{Status: "running"}
	encoded, err := json.Marshal(active)
	if err != nil {
		t.Fatalf("active marshal: %v", err)
	}
	if strings.Contains(string(encoded), "analysis_progress") {
		t.Fatalf("active payload = %s, want no analysis_progress key without progress", encoded)
	}
	active.AnalysisProgress = json.RawMessage(`{"stage":"checking_links","links_resolved":1}`)
	encoded, err = json.Marshal(active)
	if err != nil {
		t.Fatalf("active marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"analysis_progress":{"stage":"checking_links","links_resolved":1}`) {
		t.Fatalf("active payload = %s, want raw progress JSON", encoded)
	}
}

// The competitors-view snapshot reuses the shared crawl response, so a
// running competitor crawl carries the same raw progress JSON.
func TestCompetitorCrawlResponseCarriesAnalysisProgress(t *testing.T) {
	progress := []byte(`{"stage":"checking_links","links_resolved":640000}`)
	response := newCompetitorCrawlResponse(sqlc.ListProjectCompetitorsWithCrawlForUserRow{
		CrawlStatus:           pgtype.Text{String: "running", Valid: true},
		CrawlAnalysisProgress: progress,
	})
	if string(response.AnalysisProgress) != string(progress) {
		t.Fatalf("competitor progress = %s, want the raw persisted JSON", response.AnalysisProgress)
	}
	legacy := newCompetitorCrawlResponse(sqlc.ListProjectCompetitorsWithCrawlForUserRow{
		CrawlStatus: pgtype.Text{String: "completed", Valid: true},
	})
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("competitor marshal: %v", err)
	}
	if strings.Contains(string(encoded), "analysis_progress") {
		t.Fatalf("competitor payload = %s, want no analysis_progress key for legacy rows", encoded)
	}
}
