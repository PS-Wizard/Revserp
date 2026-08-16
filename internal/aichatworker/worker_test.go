package aichatworker

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
)

func TestComposeSystemContext(t *testing.T) {
	completedAt := pgtype.Timestamptz{Valid: true}
	context := composeSystemContext("selected prompt", "Example", "https://example.com", completedAt)
	for _, want := range []string{"selected prompt", "--- Project context ---", "Name: Example", "URL: https://example.com", "--- Crawl context ---"} {
		if !strings.Contains(context, want) {
			t.Errorf("system context missing %q: %q", want, context)
		}
	}
	if strings.Count(context, "selected prompt") != 1 {
		t.Errorf("system prompt appears more than once: %q", context)
	}
}

func TestAllowedTools(t *testing.T) {
	all := allowedTools(nil)
	if len(all) != 1 || all[0].Name != "read_issues" || all[0].Description == "" || len(all[0].Schema) == 0 {
		t.Fatalf("allowedTools(nil) = %+v, want the full catalog", all)
	}
	if got := allowedTools([]string{"read_issues"}); len(got) != 0 {
		t.Fatalf("allowedTools(disabled) = %+v, want none", got)
	}
	if got := allowedTools([]string{"unknown", "read_issues"}); len(got) != 0 {
		t.Fatalf("allowedTools(unknown+disabled) = %+v, want none", got)
	}
}

func TestTrimLiveToBudget(t *testing.T) {
	tools := allowedTools(nil)
	messages := []ai.Message{
		{Role: ai.RoleSystem, Content: strings.Repeat("s", liveBudgetBytes/2)},
		{Role: ai.RoleTool, Content: strings.Repeat("x", liveBudgetBytes/2), ToolCallID: "old", Name: "read_issues"},
		{Role: ai.RoleTool, Content: "kept", ToolCallID: "new", Name: "read_issues"},
	}
	if !trimLiveToBudget(messages, tools) {
		t.Fatal("trim should fit the budget")
	}
	if messages[1].Content != stubbedToolContent {
		t.Fatalf("oldest tool result not stubbed: %q", messages[1].Content)
	}
	if messages[2].Content != "kept" {
		t.Fatalf("newest tool result should be kept: %q", messages[2].Content)
	}
	// once every tool result is stubbed, the untrimmable skeleton decides
	oversized := []ai.Message{{Role: ai.RoleSystem, Content: strings.Repeat("x", liveBudgetBytes+1)}}
	if trimLiveToBudget(oversized, nil) {
		t.Fatal("oversized skeleton should not fit")
	}
}

func TestCapToolResultContent(t *testing.T) {
	if got := capToolResultContent("short"); got != "short" {
		t.Fatalf("cap changed short content: %q", got)
	}
	got := capToolResultContent(strings.Repeat("x", toolResultContentCap+100))
	if want := toolResultContentCap + len("\u2026"); len(got) != want {
		t.Fatalf("cap length = %d, want %d", len(got), want)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Fatalf("cap missing truncation marker: %q", got[len(got)-16:])
	}
}
