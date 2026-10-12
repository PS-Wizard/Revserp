package aichatworker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
)

func TestTranscriptRoundTripKeepsReasoning(t *testing.T) {
	live := []ai.Message{
		{Role: ai.RoleAssistant, Content: "working", ReasoningContent: "exact provider reasoning", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page", Args: `{"url":"https://example.com"}`}}},
		{Role: ai.RoleTool, Content: "page text", ToolCallID: "c1", Name: "read_page"},
		{Role: ai.RoleAssistant, Content: "final answer", ReasoningContent: "final reasoning"},
		{Role: ai.RoleUser, Content: "seen", Images: []ai.Image{{MediaType: "image/png", Data: "abc123"}}},
	}
	encoded, err := json.Marshal(snapshotTranscriptMessages(live))
	if err != nil {
		t.Fatal(err)
	}
	var plain []ai.Message
	if err := json.Unmarshal(encoded, &plain); err != nil {
		t.Fatal(err)
	}
	if plain[0].ReasoningContent != "" {
		t.Fatal("test premise broken: ai.Message kept reasoning")
	}
	restored := restoreTranscriptMessages(mustDecodeTranscript(t, encoded))
	if !reflect.DeepEqual(restored, live) {
		t.Fatalf("restored=%+v, want %+v", restored, live)
	}
}

func mustDecodeTranscript(t *testing.T, encoded []byte) []transcriptMessage {
	t.Helper()
	var stored []transcriptMessage
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestAssistantRoundKindSeparatesProgressFromAnswer(t *testing.T) {
	if assistantRoundKind(true) != "activity" {
		t.Fatal("tool round must classify as activity")
	}
	if assistantRoundKind(false) != "answer" {
		t.Fatal("tool-free round must classify as answer")
	}
}

func TestUpsertActivityTextAccumulatesOneRound(t *testing.T) {
	var blocks []byte
	blocks = upsertAssistantActivityText(blocks, 0, "Hello.")
	blocks = upsertAssistantActivityText(blocks, 0, " More.")
	blocks = upsertAssistantActivityText(blocks, 1, "Second.")
	activity := parseAssistantActivityBlocks(blocks)
	if len(activity) != 2 {
		t.Fatalf("activity=%v, want 2 rounds", activity)
	}
	if activity[0].Text != "Hello. More." || activity[0].Status != "running" {
		t.Errorf("round 0=%+v, want accumulated running text", activity[0])
	}
	for _, block := range activity {
		if block.Type != "activity" {
			t.Errorf("block=%+v, want type activity", block)
		}
	}
}

func TestUpsertActivityTextPreservesUnknownEntries(t *testing.T) {
	blocks := []byte(`[{"type":"image","media_type":"image/png","data":"abc"},{"type":"activity","round":0,"text":"Hi","status":"running"}]`)
	merged := upsertAssistantActivityText(blocks, 0, " there.")
	if !strings.Contains(string(merged), `"type":"image"`) {
		t.Fatalf("unknown entries must survive: %s", merged)
	}
	activity := parseAssistantActivityBlocks(merged)
	if len(activity) != 1 || activity[0].Text != "Hi there." {
		t.Fatalf("activity=%v, want merged round text", activity)
	}
}

func TestCompleteAndRemoveActivityRound(t *testing.T) {
	var blocks []byte
	blocks = upsertAssistantActivityText(blocks, 0, "progress")
	blocks = upsertAssistantActivityText(blocks, 1, "answer draft")
	blocks = completeAssistantActivityRoundRaw(blocks, 0, "progress full")
	activity := parseAssistantActivityBlocks(blocks)
	if len(activity) != 2 || activity[0].Status != "complete" || activity[0].Text != "progress full" {
		t.Fatalf("activity=%v, want round 0 pinned complete", activity)
	}
	blocks = removeAssistantActivityRoundRaw(blocks, 1)
	activity = parseAssistantActivityBlocks(blocks)
	if len(activity) != 1 || activity[0].Round != 0 {
		t.Fatalf("activity=%v, want only round 0 left", activity)
	}
}

func TestMaxActivityRoundStartsBelowZero(t *testing.T) {
	if got := maxAssistantActivityRoundRaw(nil); got != -1 {
		t.Fatalf("empty=%d, want -1", got)
	}
	blocks := completeAssistantActivityRoundRaw(nil, 2, "x")
	if got := maxAssistantActivityRoundRaw(blocks); got != 2 {
		t.Fatalf("max=%d, want 2", got)
	}
}

func TestLabelToolHistoryPassesCompletedThrough(t *testing.T) {
	if got := labelToolHistory("verbatim", "read_page", "c1", "completed", "ok"); got != "verbatim" {
		t.Fatalf("completed label=%q, want verbatim", got)
	}
	failed := labelToolHistory("body", "read_page", "c1", "failed", "page missing")
	for _, want := range []string{"failed", "read_page", "c1", "page missing", "body"} {
		if !strings.Contains(failed, want) {
			t.Errorf("failed label missing %q: %q", want, failed)
		}
	}
	unknown := labelToolHistory("", "cms__update", "c9", "running", "")
	for _, want := range []string{"unknown", "do not retry", "cms__update", "c9"} {
		if !strings.Contains(unknown, want) {
			t.Errorf("unknown label missing %q: %q", want, unknown)
		}
	}
}

func TestLegacyHistoryMessagesKeepPairsValid(t *testing.T) {
	calls := []historyToolCall{
		{CallID: "c1", Name: "read_page", Args: `{"url":"https://example.com"}`, Status: "completed", ResultContent: "page", Summary: "ok"},
		{CallID: "c2", Name: "update_business_profile", Args: "broken", Status: "failed", Summary: "denied"},
	}
	got := legacyHistoryMessages("user q", nil, "Sorry, I failed.", "failed", calls)
	if len(got) != 4 {
		t.Fatalf("messages=%d, want user+assistant+2 results", len(got))
	}
	if got[0].Role != ai.RoleUser || got[1].Role != ai.RoleAssistant {
		t.Fatalf("roles=%q/%q, want user/assistant", got[0].Role, got[1].Role)
	}
	if !strings.Contains(got[1].Content, "Sorry, I failed.") {
		t.Errorf("assistant text must stay verbatim: %q", got[1].Content)
	}
	if !strings.Contains(got[1].Content, "grouping unavailable") {
		t.Errorf("grouping disclaimer missing: %q", got[1].Content)
	}
	if got[1].ReasoningContent != "" {
		t.Errorf("legacy must not fabricate reasoning: %q", got[1].ReasoningContent)
	}
	if len(got[1].ToolCalls) != 2 {
		t.Fatalf("assistant calls=%d, want 2 for valid pairs", len(got[1].ToolCalls))
	}
	byID := map[string]string{}
	for _, call := range got[1].ToolCalls {
		byID[call.ID] = call.Name
	}
	for _, message := range got[2:] {
		if message.Role != ai.RoleTool {
			t.Fatalf("role=%q, want tool", message.Role)
		}
		if byID[message.ToolCallID] == "" {
			t.Fatalf("orphaned tool result %q", message.ToolCallID)
		}
	}
	if !strings.Contains(got[3].Content, "failed") || !strings.Contains(got[3].Content, "denied") {
		t.Errorf("failed evidence must stay labelled: %q", got[3].Content)
	}
	if got[2].Content != "page" {
		t.Errorf("completed evidence must stay verbatim: %q", got[2].Content)
	}
}

func TestRenderWriteLedgerMarksAwaitingUnknown(t *testing.T) {
	dropped := []historyExchange{{writes: []ledgerWrite{
		{Turn: "a1b2c3d4", CallID: "mcp-1", Name: "mcp__write", Args: `{"post_id":"p9"}`, Status: "awaiting", Summary: ""},
		{Turn: "a1b2c3d4", CallID: "ok-1", Name: "read_page", Args: "{}", Status: "completed", Summary: "done"},
	}}}
	ledger := renderWriteLedger(dropped)
	if strings.Contains(ledger, "executed these writes") {
		t.Fatalf("header must not claim execution:\n%s", ledger)
	}
	if !strings.Contains(ledger, "unknown (recorded awaiting") || !strings.Contains(ledger, `"mcp-1"`) {
		t.Errorf("awaiting call must read unknown with raw status:\n%s", ledger)
	}
	if !strings.Contains(ledger, `"ok-1"`) || !strings.Contains(ledger, "completed: done") {
		t.Errorf("completed call must keep its outcome:\n%s", ledger)
	}
	statuses := historyStatuses(dropped)
	if !strings.Contains(statuses["mcp-1"], "unknown (recorded awaiting") {
		t.Errorf("history status must not read ongoing: %q", statuses["mcp-1"])
	}
	if statuses["ok-1"] != "completed: done" {
		t.Errorf("history status=%q, want recorded outcome", statuses["ok-1"])
	}
}

func TestAnnotateHistoryCrawlScope(t *testing.T) {
	if got := annotateHistoryCrawlScope("sys", "abc", nil); got != "sys" {
		t.Fatalf("no changes must leave system alone: %q", got)
	}
	got := annotateHistoryCrawlScope("sys", "abc123", []string{"turn 1 ran under crawl def456 (completed)"})
	for _, want := range []string{"History scope", "def456", "completed", "abc123"} {
		if !strings.Contains(got, want) {
			t.Errorf("scope annotation missing %q: %q", want, got)
		}
	}
}

func TestContextTrimNoticeNamesDroppedCount(t *testing.T) {
	if got := contextTrimNotice(0, 3); got != "" {
		t.Fatalf("no drops means no notice: %q", got)
	}
	got := contextTrimNotice(2, 1)
	for _, want := range []string{"2", "1"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice missing %q: %q", want, got)
		}
	}
}

func TestCompleteDanglingTranscriptCalls(t *testing.T) {
	transcript := []ai.Message{
		{Role: ai.RoleAssistant, Content: "working", ReasoningContent: "r", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read_page", Args: "{}"}, {ID: "c2", Name: "write_page", Args: "{}"}}},
		{Role: ai.RoleTool, Content: "page body", ToolCallID: "c1", Name: "read_page"},
	}
	calls := []historyToolCall{
		{CallID: "c1", Name: "read_page", Status: "completed", ResultContent: "page body", Summary: "ok"},
		{CallID: "c2", Name: "write_page", Status: "running", ResultContent: "", Summary: ""},
	}
	got := completeDanglingTranscriptCalls(transcript, calls)
	if len(got) != 3 {
		t.Fatalf("messages=%d, want assistant+unknown+recorded", len(got))
	}
	if got[1].ToolCallID != "c2" || !strings.Contains(got[1].Content, "outcome unknown") || !strings.Contains(got[1].Content, "Do not retry") {
		t.Errorf("dangling call must forbid blind retry: %+v", got[1])
	}
	if got[2].Content != "page body" || got[2].ToolCallID != "c1" {
		t.Errorf("recorded result must pass verbatim: %+v", got[2])
	}
	if got[0].ReasoningContent != "r" || len(got[0].ToolCalls) != 2 {
		t.Errorf("assistant round must stay intact: %+v", got[0])
	}
}

func TestHistoryStatusesPreferRecordedOutcomes(t *testing.T) {
	history := []historyExchange{{writes: []ledgerWrite{
		{CallID: "c1", Name: "read_page", Status: "completed", Summary: "ok"},
		{CallID: "c2", Name: "write_page", Status: "awaiting", Summary: ""},
	}}}
	statuses := historyStatuses(history)
	if statuses["c1"] != "completed: ok" {
		t.Errorf("completed=%q, want recorded outcome", statuses["c1"])
	}
	if !strings.Contains(statuses["c2"], "unknown (recorded awaiting") {
		t.Errorf("awaiting=%q, want unknown with raw status", statuses["c2"])
	}
}
