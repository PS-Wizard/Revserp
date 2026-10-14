package aiaudit

import (
	"context"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
)

type recordingBootstrapCompetitorExecutor struct {
	names     []string
	addStatus string
}

func (e *recordingBootstrapCompetitorExecutor) run(_ context.Context, _ *aichattools.Registry, call ai.ToolCall, _ aichattools.Scope) (string, aichattools.Result) {
	e.names = append(e.names, call.Name)
	if call.Name == businessProfileBootstrapAddTool {
		return e.addStatus, aichattools.Result{Content: `{"status":"saved, not crawled"}`, Summary: "saved competitor, not crawled"}
	}
	return "completed", aichattools.Result{Content: "ok", Summary: "ok"}
}

func TestBusinessProfileBootstrapPromptOrdersCompetitorSaves(t *testing.T) {
	prompt := businessProfileBootstrapSystemPrompt
	for _, want := range []string{
		"add_competitor",
		"never invent",
		"remaining",
		"competitor_limit_reached",
		"never crawl",
		"Make it last",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBusinessProfileBootstrapExecutesAddToolBeforeUpdate(t *testing.T) {
	streamer := &scriptedBootstrapStreamer{rounds: [][]ai.ToolCall{
		{{Name: "add_competitor", Args: `{"name":"Rival","url":"https://rival.example/"}`}},
		{{Name: "update_business_profile", Args: `{}`}},
	}}
	exec := &recordingBootstrapCompetitorExecutor{addStatus: "completed"}
	messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

	returned, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), streamer, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, newBootstrapRunBudget(), exec.run, messages, false)
	if err != nil {
		t.Fatalf("pass with add then update: %v", err)
	}
	if len(exec.names) != 2 || exec.names[0] != "add_competitor" || exec.names[1] != "update_business_profile" {
		t.Fatalf("executed = %v, want add_competitor actually executed before update_business_profile", exec.names)
	}
	assertBalancedBootstrapHistory(t, returned)
}

func TestBusinessProfileBootstrapAddFailureKeepsProfileSetup(t *testing.T) {
	streamer := &scriptedBootstrapStreamer{rounds: [][]ai.ToolCall{
		{{Name: "add_competitor", Args: `{"name":"Rival","url":"https://rival.example/"}`}},
		{{Name: "update_business_profile", Args: `{}`}},
	}}
	// Full or zero-capacity plans fail the save but must not break setup.
	exec := &recordingBootstrapCompetitorExecutor{addStatus: "failed"}
	messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

	returned, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), streamer, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, newBootstrapRunBudget(), exec.run, messages, false)
	if err != nil {
		t.Fatalf("failed add must not fail profile setup: %v", err)
	}
	assertBalancedBootstrapHistory(t, returned)
}

func TestBusinessProfileBootstrapAddsDoNotLoosenUpdateRule(t *testing.T) {
	streamer := &scriptedBootstrapStreamer{rounds: [][]ai.ToolCall{
		{{Name: "add_competitor", Args: `{"url":"https://rival.example/"}`}, {Name: "update_business_profile", Args: `{}`}, {Name: "update_business_profile", Args: `{}`}},
	}}
	exec := &recordingBootstrapCompetitorExecutor{addStatus: "completed"}
	messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

	if _, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), streamer, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, newBootstrapRunBudget(), exec.run, messages, false); err == nil {
		t.Fatal("two update calls must still fail, even with competitor saves")
	}
}

func TestBusinessProfileBootstrapSuccessRuleWithAdds(t *testing.T) {
	withAdds := []bootstrapToolResult{
		{Name: "add_competitor", Status: "completed"},
		{Name: "add_competitor", Status: "failed"},
		{Name: "update_business_profile", Status: "completed"},
	}
	if !businessProfileBootstrapUpdateSucceeded(withAdds) {
		t.Fatal("adds plus exactly one successful update must succeed")
	}
	failedAdd := []bootstrapToolResult{
		{Name: "add_competitor", Status: "failed"},
		{Name: "update_business_profile", Status: "completed"},
	}
	if !businessProfileBootstrapUpdateSucceeded(failedAdd) {
		t.Fatal("a failed add (full plan) plus one successful update must succeed")
	}
}

func TestExecuteBusinessProfileBootstrapToolRoutesAddCompetitor(t *testing.T) {
	status, result := executeBusinessProfileBootstrapTool(context.Background(), aichattools.NewRegistry(), ai.ToolCall{Name: "add_competitor", Args: `{"url":"https://rival.example/"}`}, aichattools.Scope{})
	if status != "failed" {
		t.Fatalf("status = %q, want failed without scope wiring", status)
	}
	if !strings.Contains(result.Content, "transaction support") {
		t.Fatalf("content = %q, want the call routed to the real tool", result.Content)
	}
}
