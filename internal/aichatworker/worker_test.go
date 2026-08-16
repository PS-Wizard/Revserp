package aichatworker

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
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
	if len(all) != 2 {
		t.Fatalf("allowedTools(nil) has %d tools, want 2: %+v", len(all), all)
	}
	names := map[string]bool{}
	for _, def := range all {
		names[def.Name] = true
		if def.Description == "" || len(def.Schema) == 0 {
			t.Fatalf("tool %s missing description or schema: %+v", def.Name, def)
		}
	}
	for _, name := range []string{"read_issues", "get_score_summary"} {
		if !names[name] {
			t.Fatalf("allowedTools(nil) missing %s: %+v", name, all)
		}
	}
	if got := allowedTools([]string{"read_issues"}); len(got) != 1 || got[0].Name != "get_score_summary" {
		t.Fatalf("allowedTools(disabled read_issues) = %+v, want only get_score_summary", got)
	}
	if got := allowedTools([]string{"get_score_summary"}); len(got) != 1 || got[0].Name != "read_issues" {
		t.Fatalf("allowedTools(disabled get_score_summary) = %+v, want only read_issues", got)
	}
	if got := allowedTools([]string{"read_issues", "get_score_summary"}); len(got) != 0 {
		t.Fatalf("allowedTools(all disabled) = %+v, want none", got)
	}
	if got := allowedTools([]string{"unknown", "read_issues"}); len(got) != 1 || got[0].Name != "get_score_summary" {
		t.Fatalf("allowedTools(unknown+disabled) = %+v, want only get_score_summary", got)
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

func TestNormalizeToolCallResult(t *testing.T) {
	status, result := normalizeToolCallResult("read_issues", "completed", aichattools.Result{
		Content: "read_issues error: argument \"limit\" must be at least 1",
	})
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if result.Summary != "argument \"limit\" must be at least 1" {
		t.Fatalf("summary = %q", result.Summary)
	}

	status, result = normalizeToolCallResult("read_issues", "completed", aichattools.Result{
		Content: `{"issues":[]}`,
		Summary: "0 issues shown (0 matching total)",
	})
	if status != "completed" || result.Summary != "0 issues shown (0 matching total)" {
		t.Fatalf("success path changed: status=%q summary=%q", status, result.Summary)
	}
}
