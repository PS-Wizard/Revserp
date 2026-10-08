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
	context := composeSystemContext("selected prompt", "Example", "https://example.com", completedAt, "Acme Downtown", "Kathmandu")
	for _, want := range []string{"selected prompt", "--- Editor links ---", `"revserp-editor"`, "--- Project context ---", "Name: Example", "URL: https://example.com", "--- Location context ---", "Location: Acme Downtown", "Locality: Kathmandu", "--- Crawl context ---"} {
		if !strings.Contains(context, want) {
			t.Errorf("system context missing %q: %q", want, context)
		}
	}
	if strings.Count(context, "selected prompt") != 1 {
		t.Errorf("system prompt appears more than once: %q", context)
	}
}

func TestComposeSystemContextOmitsLocationForParent(t *testing.T) {
	context := composeSystemContext("p", "Example", "https://example.com", pgtype.Timestamptz{}, "", "")
	if strings.Contains(context, "--- Location context ---") {
		t.Fatalf("parent context must omit the location section: %q", context)
	}
}

func TestAllowedTools(t *testing.T) {
	all := allowedTools(nil)
	if len(all) != 15 {
		t.Fatalf("allowedTools(nil) has %d tools, want 15: %+v", len(all), all)
	}
	names := map[string]bool{}
	for _, def := range all {
		names[def.Name] = true
		if def.Description == "" || len(def.Schema) == 0 {
			t.Fatalf("tool %s missing description or schema: %+v", def.Name, def)
		}
	}
	for _, name := range []string{"read_issues", "get_score_summary", "get_search_console_data", "get_business_profile", "read_issue_work", "read_page", "render_chart", "update_business_profile", "get_project_keywords", "update_project_keywords", "web_search", "get_search_suggestions", "fetch_url", "get_keyword_coverage", "get_location_landmarks"} {
		if !names[name] {
			t.Fatalf("allowedTools(nil) missing %s: %+v", name, all)
		}
	}
	if got := allowedTools([]string{"read_issues"}); len(got) != 14 {
		t.Fatalf("allowedTools(disabled read_issues) = %+v, want the other fourteen", got)
	}
	if got := allowedTools([]string{"read_issues", "get_score_summary", "get_search_console_data", "get_business_profile", "read_issue_work", "read_page", "render_chart", "update_business_profile", "get_project_keywords", "update_project_keywords", "web_search", "get_search_suggestions", "fetch_url", "get_keyword_coverage", "get_location_landmarks"}); len(got) != 0 {
		t.Fatalf("allowedTools(all disabled) = %+v, want none", got)
	}
	if got := allowedTools([]string{"unknown", "read_issues"}); len(got) != 14 {
		t.Fatalf("allowedTools(unknown+disabled) = %+v, want the other fourteen", got)
	}
	if got := allowedTools([]string{"update_project_keywords"}); len(got) != 14 {
		t.Fatalf("allowedTools(disabled update_project_keywords) = %+v, want the other fourteen", got)
	}
}

func TestCapToolResultContent(t *testing.T) {
	if got := capToolResultContent("short"); got != "short" {
		t.Fatalf("cap changed short content: %q", got)
	}
	got := capToolResultContent(strings.Repeat("x", toolResultContentCap+100))
	if want := toolResultContentCap + len("\u2026[truncated]"); len(got) != want {
		t.Fatalf("cap length = %d, want %d", len(got), want)
	}
	if !strings.HasSuffix(got, "\u2026[truncated]") {
		t.Fatal("cap missing truncation marker")
	}
}

func TestParseUserImages(t *testing.T) {
	if got := parseUserImages(nil); got != nil {
		t.Fatalf("nil blocks = %#v", got)
	}
	if got := parseUserImages([]byte("[]")); got != nil {
		t.Fatalf("empty array = %#v", got)
	}
	got := parseUserImages([]byte(`[{"type":"image","media_type":"image/jpeg","data":"abc"}]`))
	if len(got) != 1 || got[0] != (ai.Image{MediaType: "image/jpeg", Data: "abc"}) {
		t.Fatalf("parsed = %#v", got)
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

func TestMessageStatusForOutput(t *testing.T) {
	if got := messageStatusForOutput(true); got != "partial" {
		t.Fatalf("output=true status=%q, want partial", got)
	}
	if got := messageStatusForOutput(false); got != "failed" {
		t.Fatalf("output=false status=%q, want failed", got)
	}
}
