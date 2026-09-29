package app

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func testPhaseText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

func TestBuildCrawlResponsePhaseOnlyWhenRunning(t *testing.T) {
	running := buildCrawlResponse(pgtype.UUID{}, pgtype.UUID{}, "running", testPhaseText("analyzing"), nil, 0, 0, 0, nil, pgtype.Bool{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{})
	if running.Phase != "analyzing" {
		t.Fatalf("running crawl should keep phase, got %q", running.Phase)
	}
	for _, status := range []string{"queued", "completed", "failed", "cancelled"} {
		resp := buildCrawlResponse(pgtype.UUID{}, pgtype.UUID{}, status, testPhaseText("analyzing"), nil, 0, 0, 0, nil, pgtype.Bool{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{})
		if resp.Phase != "" {
			t.Fatalf("status %q should mask phase, got %q", status, resp.Phase)
		}
	}
}

func TestMCPCrawlRowFromPartsPhaseOnlyWhenRunning(t *testing.T) {
	running := mcpCrawlRowFromParts(pgtype.UUID{}, pgtype.UUID{}, "running", testPhaseText("analyzing"), 0, 0, 0, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{})
	if running.Phase != "analyzing" {
		t.Fatalf("running crawl should keep phase, got %q", running.Phase)
	}
	for _, status := range []string{"queued", "completed", "failed", "cancelled"} {
		row := mcpCrawlRowFromParts(pgtype.UUID{}, pgtype.UUID{}, status, testPhaseText("analyzing"), 0, 0, 0, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Int4{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{})
		if row.Phase != "" {
			t.Fatalf("status %q should mask phase, got %q", status, row.Phase)
		}
	}
}
