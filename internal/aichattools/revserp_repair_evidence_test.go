package aichattools

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestRevRepairReadPageEvidence(t *testing.T) {
	description := readPageTool().Def.Description
	for _, want := range []string{
		"SELECTED stored crawl",
		"not the live web",
		"stored_at",
		"cleaned",
		"never conclude JSON-LD is absent",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("read_page description lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(description), "live stored") {
		t.Fatal("read_page description mislabels stored content as live")
	}

	withTime := sqlc.GetCrawlPageContentByURLForUserRow{
		Url:       "https://example.com/a",
		CrawlID:   pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Valid: true},
	}
	metadata := makeReadPageMetadata(withTime)
	if metadata.StoredAt == "" {
		t.Fatal("stored_at missing when CreatedAt is valid")
	}
	if _, err := time.Parse(time.RFC3339, metadata.StoredAt); err != nil {
		t.Fatalf("stored_at is not RFC3339: %q", metadata.StoredAt)
	}
	if metadata.CrawlID == "" {
		t.Fatal("crawl_id missing from stored evidence")
	}

	withoutTime := sqlc.GetCrawlPageContentByURLForUserRow{Url: "https://example.com/a"}
	if got := makeReadPageMetadata(withoutTime); got.StoredAt != "" {
		t.Fatalf("stored_at = %q, want omitted when CreatedAt is invalid", got.StoredAt)
	}
}

func TestRevRepairReadIssuesSkillPointer(t *testing.T) {
	description := readIssuesTool().Def.Description
	for _, want := range []string{
		"revserp-issue-repair",
		"references/seo.md",
		"references/aeo.md",
		"references/pagespeed.md",
		"references/evidence.md",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("read_issues description lacks %q", want)
		}
	}
}

func TestRevRepairWordPressPreviewUncertainty(t *testing.T) {
	var preview *wordpressTool
	for i := range wordpressTools {
		if wordpressTools[i].Name == "render_page_preview" {
			preview = &wordpressTools[i]
			break
		}
	}
	if preview == nil {
		t.Fatal("render_page_preview missing from reviewed catalogue")
	}
	description := wordPressToolDescription(*preview)
	for _, want := range []string{
		"not proof",
		"unless the provider explicitly supplies it",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("preview description lacks %q: %q", want, description)
		}
	}
	if strings.Contains(strings.ToLower(description), "all mcp servers strip") ||
		strings.Contains(strings.ToLower(description), "every mcp server strips") ||
		strings.Contains(strings.ToLower(description), "all servers strip") {
		t.Fatalf("preview description overclaims server behavior: %q", description)
	}
}

func TestRevRepairUnknownMCPGuardIntact(t *testing.T) {
	if !strings.Contains(mcpUnreviewedToolNote, "approval policy") {
		t.Fatal("unreviewed MCP note lost the approval-policy guard")
	}
	if !strings.Contains(mcpUnreviewedResultNote, "untrusted server data") {
		t.Fatal("unreviewed MCP result note lost the untrusted-data guard")
	}
}
