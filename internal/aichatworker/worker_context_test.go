package aichatworker

import (
	"encoding/json"
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

// TestFitHistoryExchangesKeepsOrder checks chronological order is preserved
// around the mandatory system/current messages.
func TestFitHistoryExchangesKeepsOrder(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	pair := func(user, assistant string) historyExchange {
		return historyExchange{messages: []ai.Message{
			{Role: ai.RoleUser, Content: user},
			{Role: ai.RoleAssistant, Content: assistant},
		}}
	}
	newestFirst := []historyExchange{pair("newest", "a3"), pair("middle", "a2"), pair("oldest", "a1")}
	got, dropped, _ := fitHistoryExchanges(system, current, newestFirst, "", "high", nil)
	if dropped != 0 {
		t.Fatalf("dropped=%d, want 0", dropped)
	}
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

// TestFitHistoryExchangesDropsOldestFirst uses the small legacy capacity so
// only the newest exchange still fits; an oversized middle exchange stops the
// scan without pulling older exchanges past the gap.
func TestFitHistoryExchangesDropsOldestFirst(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	pair := func(user, assistant string) historyExchange {
		return historyExchange{messages: []ai.Message{
			{Role: ai.RoleUser, Content: user},
			{Role: ai.RoleAssistant, Content: assistant},
		}}
	}
	big := strings.Repeat("w", 300000)
	newestFirst := []historyExchange{pair("newest "+big, "a3"), pair("middle "+big, "a2"), pair("oldest "+big, "a1")}
	got, dropped, _ := fitHistoryExchanges(system, current, newestFirst, "legacy-model", "high", nil)
	found := map[string]bool{}
	for _, message := range got {
		for _, name := range []string{"newest", "middle", "oldest"} {
			if strings.HasPrefix(message.Content, name) {
				found[name] = true
			}
		}
	}
	if !found["newest"] || found["middle"] || found["oldest"] {
		t.Fatalf("want only newest exchange kept, found=%v", found)
	}
	if dropped != 2 {
		t.Fatalf("dropped=%d, want 2", dropped)
	}

	huge := strings.Repeat("w", 5000000)
	mixed := []historyExchange{pair("newest", "a3"), pair(huge, huge), pair("oldest", "a1")}
	got, _, _ = fitHistoryExchanges(system, current, mixed, "", "high", nil)
	var order []string
	for _, message := range got {
		order = append(order, message.Content)
	}
	joined := strings.Join(order, "\n")
	if strings.Contains(joined, "oldest") || !strings.Contains(joined, "newest") {
		t.Fatal("history must retain the newest contiguous exchanges, not older exchanges beyond a gap")
	}
	if strings.Contains(joined, huge[:100]) {
		t.Fatal("oversized exchange must not be selected")
	}
}

// TestFitHistoryExchangesChargesToolSchemaOnce pins the capacity regression:
// many tiny exchanges plus a large tool schema must all fit under the actual
// request limit. The schema is charged once, never per exchange.
func TestFitHistoryExchangesChargesToolSchemaOnce(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	schemaDesc := strings.Repeat("a", 300000)
	tools := []ai.ToolDef{{Name: "big_tool", Description: schemaDesc, Schema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string"}}}`)}}
	newestFirst := make([]historyExchange, 0, 20)
	for i := 0; i < 20; i++ {
		newestFirst = append(newestFirst, historyExchange{messages: []ai.Message{
			{Role: ai.RoleUser, Content: "tiny question"},
			{Role: ai.RoleAssistant, Content: "tiny answer", ReasoningContent: "tiny reasoning", ToolCalls: []ai.ToolCall{{ID: "c", Name: "big_tool", Args: "{}"}}},
			{Role: ai.RoleTool, Content: "tiny result", ToolCallID: "c", Name: "big_tool"},
		}})
	}
	got, dropped, newestOver := fitHistoryExchanges(system, current, newestFirst, "deepseek-flash", "high", tools)
	if newestOver {
		t.Fatal("newest tiny exchange must fit its own guard")
	}
	if dropped != 0 {
		t.Fatalf("dropped=%d, want 0: schema must not crowd out tiny history", dropped)
	}
	request := ai.Request{Model: "deepseek-flash", Effort: "high", Messages: got, Tools: tools}
	if tokens := ai.EstimateChatInputTokens(request); tokens > chatInputBudgetTokens("deepseek-flash") {
		t.Fatalf("fitted request=%d tokens over budget=%d", tokens, chatInputBudgetTokens("deepseek-flash"))
	}
}

// TestFitHistoryExchangesFlagsOversizeNewest forces the explicit failure path:
// a newest exchange alone over the guard must report over instead of letting
// the terminal stub trim it into a pseudo fit.
func TestFitHistoryExchangesFlagsOversizeNewest(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	newestFirst := []historyExchange{{messages: []ai.Message{
		{Role: ai.RoleUser, Content: "newest"},
		{Role: ai.RoleAssistant, Content: "answer", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page", Args: "{}"}}},
		{Role: ai.RoleTool, Content: strings.Repeat("result data ", 200000), ToolCallID: "c1", Name: "read_page"},
	}}}
	_, _, newestOver := fitHistoryExchanges(system, current, newestFirst, "legacy-model", "high", nil)
	if !newestOver {
		t.Fatal("oversize newest exchange must report over-guard")
	}
	_, notice, over := fitHistoryWithLedger(system, current, []historyExchange{newestFirst[0]}, "legacy-model", "high", nil)
	if !over || notice == "" {
		t.Fatalf("over=%v notice=%q, want explicit failure signal", over, notice)
	}
}

// TestFitHistoryWithLedgerKeepsOmittedWrites lists executed writes from dropped
// exchanges in the system prompt under the same guard, with full args so a
// trailing target ID stays identifiable.
func TestFitHistoryWithLedgerKeepsOmittedWrites(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	bigArgs := `{"html": "` + strings.Repeat("x", 50000) + `", "post_id": "trailing-target-9"}`
	oldestFirst := []historyExchange{
		{messages: []ai.Message{
			{Role: ai.RoleUser, Content: "old"},
			{Role: ai.RoleAssistant, Content: "wrote", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "update_page", Args: bigArgs}}},
			{Role: ai.RoleTool, Content: strings.Repeat("result data ", 200000), ToolCallID: "c1", Name: "update_page"},
		},
			writes: []ledgerWrite{{Turn: "old12345", CallID: "c1", Name: "update_page", Args: bigArgs, Status: "completed", Summary: "page updated"}}},
		{messages: []ai.Message{
			{Role: ai.RoleUser, Content: "new"},
			{Role: ai.RoleAssistant, Content: "done"},
		}},
	}
	got, notice, over := fitHistoryWithLedger(system, current, oldestFirst, "legacy-model", "high", nil)
	if over {
		t.Fatal("newest small exchange must fit")
	}
	if notice == "" || !strings.Contains(notice, "system prompt") {
		t.Fatalf("notice=%q, want ledger pointer", notice)
	}
	if !strings.Contains(got[0].Content, "Omitted write ledger") {
		t.Fatalf("system missing ledger:\n%s", got[0].Content)
	}
	for _, want := range []string{"update_page", "c1", "completed", "page updated", "trailing-target-9"} {
		if !strings.Contains(got[0].Content, want) {
			t.Errorf("ledger missing %q", want)
		}
	}
	if !strings.Contains(got[0].Content, bigArgs) {
		t.Error("ledger missing full large-write args")
	}
	request := ai.Request{Messages: got}
	if ai.EstimateChatInputTokens(request) > chatInputBudgetTokens("legacy-model") {
		t.Fatal("ledger fit must hold the same guard")
	}
}

// TestFitHistoryWithLedgerLeavesFullContextAlone keeps read-only full history
// byte-identical when everything fits.
func TestFitHistoryWithLedgerLeavesFullContextAlone(t *testing.T) {
	system := ai.Message{Role: "system", Content: "sys"}
	current := ai.Message{Role: "user", Content: "now"}
	oldestFirst := []historyExchange{
		{messages: []ai.Message{
			{Role: ai.RoleUser, Content: "old"},
			{Role: ai.RoleAssistant, Content: "a", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page", Args: "{}"}}},
			{Role: ai.RoleTool, Content: "r", ToolCallID: "c1", Name: "read_page"},
		},
			writes: []ledgerWrite{{Turn: "old", CallID: "c1", Name: "read_page", Args: "{}", Status: "completed", Summary: "ok"}}},
	}
	got, notice, over := fitHistoryWithLedger(system, current, oldestFirst, "deepseek-flash", "high", nil)
	if over || notice != "" {
		t.Fatalf("over=%v notice=%q, want clean full fit", over, notice)
	}
	if got[0].Content != "sys" {
		t.Fatalf("system=%q, want untouched", got[0].Content)
	}
	if len(got) != 5 {
		t.Fatalf("messages=%d, want system+3+current", len(got))
	}
}

// TestFitHistoryWithLedgerNeverOrphansToolResults keeps each assistant call
// with its results when capacity forces a cut.
func TestFitHistoryWithLedgerNeverOrphansToolResults(t *testing.T) {
	system := ai.Message{Role: ai.RoleSystem, Content: "sys"}
	current := ai.Message{Role: ai.RoleUser, Content: "now"}
	oldestFirst := []historyExchange{
		{messages: []ai.Message{
			{Role: ai.RoleUser, Content: "old"},
			{Role: ai.RoleAssistant, Content: "working", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page", Args: "{}"}}},
			{Role: ai.RoleTool, Content: strings.Repeat("result data ", 200000), ToolCallID: "c1", Name: "read_page"},
		},
			writes: []ledgerWrite{{Turn: "old", CallID: "c1", Name: "read_page", Args: "{}", Status: "completed", Summary: "ok"}}},
		{messages: []ai.Message{
			{Role: ai.RoleUser, Content: "new"},
			{Role: ai.RoleAssistant, Content: "also working", ToolCalls: []ai.ToolCall{{ID: "c2", Name: "read_page", Args: "{}"}}},
			{Role: ai.RoleTool, Content: "small", ToolCallID: "c2", Name: "read_page"},
		},
			writes: []ledgerWrite{{Turn: "new", CallID: "c2", Name: "read_page", Args: "{}", Status: "completed", Summary: "ok"}}},
	}
	got, notice, over := fitHistoryWithLedger(system, current, oldestFirst, "legacy-model", "high", nil)
	if over {
		t.Fatal("newest small exchange must fit")
	}
	if notice == "" {
		t.Fatal("dropping history must produce an explicit context notice")
	}
	joined := ""
	for _, message := range got {
		joined += "\n" + message.Content
	}
	if strings.Contains(joined, "result data result") {
		t.Fatal("oldest exchange result must drop whole, never partially")
	}
	if !strings.Contains(joined, "new") || !strings.Contains(joined, "small") {
		t.Fatalf("newest exchange must stay whole: %q", joined)
	}
	if !strings.Contains(got[0].Content, "read_page") || !strings.Contains(got[0].Content, `"c1"`) {
		t.Fatalf("omitted old write must survive in the ledger:\n%s", got[0].Content)
	}
	// Every kept tool result still follows its assistant call.
	wantCall := ""
	for _, message := range got {
		for _, call := range message.ToolCalls {
			wantCall = call.ID
		}
		if message.Role == ai.RoleTool && message.ToolCallID != wantCall && wantCall != "" {
			t.Fatalf("orphaned tool result %q without its call", message.ToolCallID)
		}
		if message.Role == ai.RoleUser {
			wantCall = ""
		}
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
