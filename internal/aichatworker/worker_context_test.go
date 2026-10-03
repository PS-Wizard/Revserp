package aichatworker

import (
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
)

// TestChatLargeToolResultSurvivesCapacity is the byte-guard regression: more
// than 192KiB of legitimate tool output must fit model token capacity, and
// building the terminal clone must never mutate live evidence.
func TestChatLargeToolResultSurvivesCapacity(t *testing.T) {
	live := []ai.Message{
		{Role: ai.RoleSystem, Content: "system"},
		{Role: ai.RoleAssistant, Content: "working", ReasoningContent: "private", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_issues", Args: "{}"}}},
		{Role: ai.RoleTool, Content: strings.Repeat("result data ", 25000), ToolCallID: "c1", Name: "read_issues"},
		{Role: ai.RoleUser, Content: "next question"},
	}
	if len(live[2].Content) <= 192<<10 {
		t.Fatal("test setup: tool result must exceed the old 192KiB guard")
	}
	tracker := &chatContextTracker{}
	req := ai.Request{Model: "", Effort: "high", Messages: live, Tools: allowedTools(nil)}
	if got, budget := tracker.inputTokens(req, live), chatInputBudgetTokens(""); got > budget {
		t.Fatalf("large valid request exceeds capacity: input_tokens=%d budget_tokens=%d", got, budget)
	}
	before := live[2].Content
	if _, _, ok := terminalNoToolsMessages(live, map[string]string{"c1": "completed: ok"}, ""); !ok {
		t.Fatal("large valid request must still allow a terminal response")
	}
	if live[2].Content != before {
		t.Fatal("terminal path mutated the live transcript")
	}
}

// TestChatReasoningCountsOnlyWhenReplayed pins the wire predicate to the
// accounting: reasoning counts in tools mode and is ignored without tools or
// with effort none, where the provider never sends it.
func TestChatReasoningCountsOnlyWhenReplayed(t *testing.T) {
	reasoning := strings.Repeat("private reasoning ", 4000)
	messages := []ai.Message{{Role: ai.RoleAssistant, Content: "hi", ReasoningContent: reasoning}}
	withTools := ai.Request{Effort: "high", Messages: messages, Tools: allowedTools(nil)}
	noTools := ai.Request{Effort: "high", Messages: messages}
	noEffort := ai.Request{Effort: "none", Messages: messages, Tools: allowedTools(nil)}
	if !ai.ChatReplaysReasoning(withTools) {
		t.Fatal("reasoning must replay for tools requests")
	}
	if ai.ChatReplaysReasoning(noTools) || ai.ChatReplaysReasoning(noEffort) {
		t.Fatal("reasoning must not replay without tools or with effort none")
	}
	toolsTokens := ai.EstimateChatInputTokens(withTools)
	bareTokens := ai.EstimateChatInputTokens(noTools)
	if toolsTokens <= bareTokens {
		t.Fatalf("tools estimate=%d must exceed no-tools estimate=%d", toolsTokens, bareTokens)
	}
	silent := ai.Request{Effort: "high", Messages: []ai.Message{{Role: ai.RoleAssistant, Content: "hi"}}}
	if bareTokens != ai.EstimateChatInputTokens(silent) {
		t.Fatal("no-tools accounting must ignore retained reasoning")
	}
}

// TestChatContinuationCountsCompletionOnce pins the usage anchor: the next
// input is last Prompt + last Completion (which already includes Reasoning)
// plus appended messages, never Prompt + Completion + Reasoning again. A
// changed request shape falls back to the estimator.
func TestChatContinuationCountsCompletionOnce(t *testing.T) {
	live := []ai.Message{
		{Role: ai.RoleSystem, Content: "system"},
		{Role: ai.RoleUser, Content: "check the page"},
		{Role: ai.RoleAssistant, Content: strings.Repeat("working ", 1000), ReasoningContent: strings.Repeat("private ", 1000), ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page", Args: `{"url":"https://example.com"}`}}},
		{Role: ai.RoleTool, Content: "new result", ToolCallID: "c1", Name: "read_page"},
	}
	tracker := &chatContextTracker{}
	tracker.noteSent(true, "high", 2)
	tracker.noteUsage(ai.Usage{Prompt: 1000, Reasoning: 400, Completion: 700})
	req := ai.Request{Effort: "high", Messages: live, Tools: allowedTools(nil)}
	envelopes := []ai.Message{
		{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page"}}},
		live[3],
	}
	appended := ai.EstimateChatInputTokens(ai.Request{Messages: envelopes})
	if got, want := tracker.inputTokens(req, live), 1000+700+appended; got != want {
		t.Fatalf("continuation=%d, want prompt+completion+new envelopes/results=%d", got, want)
	}
	if live[2].Content == "" || live[2].ReasoningContent == "" || live[2].ToolCalls[0].Args == "" {
		t.Fatal("accounting changed live assistant output")
	}
	changed := ai.Request{Effort: "none", Messages: live}
	if got, want := tracker.inputTokens(changed, live), ai.EstimateChatInputTokens(changed); got != want {
		t.Fatalf("changed shape=%d, want estimator fallback=%d", got, want)
	}
	fresh := &chatContextTracker{}
	if got, want := fresh.inputTokens(req, live), ai.EstimateChatInputTokens(req); got != want {
		t.Fatalf("no usage=%d, want estimator fallback=%d", got, want)
	}
	tracker.noteSent(true, "high", len(live))
	if got, want := tracker.inputTokens(req, live), ai.EstimateChatInputTokens(req); got != want {
		t.Fatalf("request without fresh usage reused stale counts: got=%d want=%d", got, want)
	}
}

// TestSelectHistoryPairsKeepsOrder checks chronological order is preserved
// around the mandatory system/current messages.
func TestSelectHistoryPairsKeepsOrder(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	newestFirst := []historyPair{
		{user: "newest", assistant: "a3"},
		{user: "middle", assistant: "a2"},
		{user: "oldest", assistant: "a1"},
	}
	got := selectHistoryPairs(system, current, newestFirst, "", "high", nil)
	want := []string{"sys", "oldest", "a1", "middle", "a2", "newest", "a3", "now"}
	if len(got) != len(want) {
		t.Fatalf("messages=%d, want %d", len(got), len(want))
	}
	for i, content := range want {
		if got[i].Content != content {
			t.Fatalf("message %d=%q, want %q", i, got[i].Content, content)
		}
	}
}

// TestSelectHistoryPairsDropsOldestFirst uses the small legacy capacity so
// only the newest pair still fits; an oversized middle pair is skipped
// without dropping older pairs that fit.
func TestSelectHistoryPairsDropsOldestFirst(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	big := strings.Repeat("w", 300000)
	newestFirst := []historyPair{
		{user: "newest " + big, assistant: "a3"},
		{user: "middle " + big, assistant: "a2"},
		{user: "oldest " + big, assistant: "a1"},
	}
	got := selectHistoryPairs(system, current, newestFirst, "legacy-model", "high", nil)
	found := map[string]bool{}
	for _, message := range got {
		for _, name := range []string{"newest", "middle", "oldest"} {
			if strings.HasPrefix(message.Content, name) {
				found[name] = true
			}
		}
	}
	if !found["newest"] || found["middle"] || found["oldest"] {
		t.Fatalf("want only newest pair kept, found=%v", found)
	}

	huge := strings.Repeat("w", 5000000)
	mixed := []historyPair{
		{user: "newest", assistant: "a3"},
		{user: huge, assistant: huge},
		{user: "oldest", assistant: "a1"},
	}
	got = selectHistoryPairs(system, current, mixed, "", "high", nil)
	var order []string
	for _, message := range got {
		order = append(order, message.Content)
	}
	joined := strings.Join(order, "\n")
	if strings.Contains(joined, "oldest") || !strings.Contains(joined, "newest") {
		t.Fatal("history must retain the newest contiguous pairs, not older pairs beyond a gap")
	}
	if strings.Contains(joined, huge[:100]) {
		t.Fatal("oversized pair must not be selected")
	}
}

// TestTerminalNoToolsPreservesEvidence forces a terminal shrink with legacy
// capacity: oldest results stub first with call identity and known
// status/summary, while reasoning, tool calls, and live stay intact and the
// prompt stays honest about unfinished work.
func TestTerminalNoToolsPreservesEvidence(t *testing.T) {
	big := strings.Repeat("result data ", 25000)
	live := []ai.Message{
		{Role: ai.RoleSystem, Content: "sys"},
		{Role: ai.RoleAssistant, Content: "text", ReasoningContent: "keep me", ToolCalls: []ai.ToolCall{
			{ID: "c1", Name: "read_issues", Args: "{}"},
			{ID: "c2", Name: "read_page", Args: "{}"},
		}},
		{Role: ai.RoleTool, Content: big, ToolCallID: "c1", Name: "read_issues"},
		{Role: ai.RoleTool, Content: big, ToolCallID: "c2", Name: "read_page"},
	}
	rundown := map[string]string{}
	recordToolRundown(rundown, "c1", "completed", "3 issues shown")
	recordToolRundown(rundown, "c2", "failed", "page missing")
	clone, _, ok := terminalNoToolsMessages(live, rundown, "legacy-model")
	if !ok {
		t.Fatal("terminal response must fit after shrinking older results")
	}
	if len(clone) != len(live)+1 {
		t.Fatalf("terminal clone=%d messages, want %d", len(clone), len(live)+1)
	}
	if clone[2].Content == big || !strings.Contains(clone[2].Content, "read_issues") {
		t.Fatalf("oldest result must stub with call identity: %q", clone[2].Content[:100])
	}
	for _, want := range []string{"completed", "3 issues shown"} {
		if !strings.Contains(clone[2].Content, want) {
			t.Fatalf("stub must retain status/summary, missing %q: %q", want, clone[2].Content)
		}
	}
	if clone[3].Content != big {
		t.Fatal("newest result must be kept intact")
	}
	if clone[1].ReasoningContent != "keep me" || len(clone[1].ToolCalls) != 2 {
		t.Fatal("reasoning and tool calls must never be removed")
	}
	if live[2].Content != big {
		t.Fatal("live transcript must be preserved unmutated")
	}
	prompt := clone[len(clone)-1].Content
	for _, want := range []string{"Do not request any more tools", "unfinished", "unknown", "failed", "Do not claim overall success"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("terminal prompt must stay honest, missing %q", want)
		}
	}
}

// TestTerminalNoToolsFailsLocally covers the fatal guard: when even mandatory
// context exceeds capacity the caller finalizes context_too_large.
func TestTerminalNoToolsFailsLocally(t *testing.T) {
	live := []ai.Message{{Role: ai.RoleSystem, Content: strings.Repeat("w", 500000)}}
	_, tokens, ok := terminalNoToolsMessages(live, nil, "legacy-model")
	if ok {
		t.Fatal("unfittable mandatory context must report failure")
	}
	if tokens <= chatInputBudgetTokens("legacy-model") {
		t.Fatalf("reported tokens=%d must exceed budget", tokens)
	}
}

func TestChatCapacityFallsBackToTerminalReport(t *testing.T) {
	live := []ai.Message{
		{Role: ai.RoleSystem, Content: "system"},
		{Role: ai.RoleUser, Content: "finish the work"},
		{Role: ai.RoleAssistant, ReasoningContent: "private", ToolCalls: []ai.ToolCall{{ID: "write1", Name: "write_content", Args: "{}"}}},
		{Role: ai.RoleTool, ToolCallID: "write1", Name: "write_content", Content: "call outcome unknown"},
	}
	tracker := &chatContextTracker{}
	tracker.noteSent(true, "high", 2)
	tracker.noteUsage(ai.Usage{Prompt: ai.ChatContextCapacity("deepseek-flash") - ai.ChatMaxOutputTokens, Completion: 20, Reasoning: 10})
	req := ai.Request{Model: "deepseek-flash", Effort: "high", Messages: live, Tools: allowedTools(nil)}
	if tracker.inputTokens(req, live) <= chatInputBudgetTokens(req.Model) {
		t.Fatal("active tool request must exceed remaining model capacity")
	}
	terminal, _, ok := terminalNoToolsMessages(live, map[string]string{"write1": "failed: call outcome unknown"}, req.Model)
	if !ok || terminal[2].ReasoningContent != "private" || terminal[3].Content != "call outcome unknown" {
		t.Fatal("terminal report must fit without deleting reasoning or write outcomes")
	}
	if ai.ChatReplaysReasoning(ai.Request{Effort: "high", Messages: terminal}) {
		t.Fatal("terminal report must not replay reasoning or offer tools")
	}
}
