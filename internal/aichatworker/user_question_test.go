package aichatworker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
)

// Question events wrap the entity as {question_request: entity}.
func TestUserQuestionEventPayloadShape(t *testing.T) {
	questions := json.RawMessage(`[{"id":"q1","header":"Scope","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`)
	for _, event := range []string{"question_required", "question_resolved"} {
		_ = event
	}
	payload, err := marshalUserQuestionEvent("req", "turn", "call-1", "pending", questions, nil, "", time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 {
		t.Fatalf("event payload has %d top-level keys, want exactly question_request: %s", len(decoded), payload)
	}
	raw, ok := decoded["question_request"]
	if !ok {
		t.Fatalf("event payload missing question_request: %s", payload)
	}
	var entity map[string]any
	if err := json.Unmarshal(raw, &entity); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "turn_id", "tool_call_id", "status", "questions", "created_at"} {
		if _, ok := entity[key]; !ok {
			t.Errorf("entity missing %q: %s", key, raw)
		}
	}
}

func TestUserQuestionMessagesRoundTripKeepsReasoning(t *testing.T) {
	live := []ai.Message{
		{Role: ai.RoleSystem, Content: "system"},
		{Role: ai.RoleUser, Content: "question"},
		{
			Role:             ai.RoleAssistant,
			Content:          "thinking aloud",
			ReasoningContent: "exact provider reasoning that ai.Message drops from JSON",
			ToolCalls:        []ai.ToolCall{{ID: "call-1", Name: "ask_user_question", Args: `{"questions":[]}`}},
		},
		{Role: ai.RoleTool, Content: "done", ToolCallID: "call-0", Name: "read_page"},
	}
	encoded, err := json.Marshal(snapshotUserQuestionMessages(live))
	if err != nil {
		t.Fatal(err)
	}
	// ai.Message drops ReasoningContent from JSON; the snapshot keeps it.
	var plain []ai.Message
	if err := json.Unmarshal(encoded, &plain); err != nil {
		t.Fatal(err)
	}
	if plain[2].ReasoningContent != "" {
		t.Fatalf("test premise broken: ai.Message kept reasoning %q", plain[2].ReasoningContent)
	}
	var stored []userQuestionMessage
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	restored := restoreUserQuestionMessages(stored)
	if len(restored) != len(live) {
		t.Fatalf("restored %d messages, want %d", len(restored), len(live))
	}
	for i := range live {
		a, b := live[i], restored[i]
		if a.Role != b.Role || a.Content != b.Content || a.ToolCallID != b.ToolCallID || a.Name != b.Name || a.ReasoningContent != b.ReasoningContent {
			t.Errorf("message %d differs: %+v vs %+v", i, a, b)
		}
		if len(a.ToolCalls) != len(b.ToolCalls) {
			t.Errorf("message %d tool calls differ: %+v vs %+v", i, a.ToolCalls, b.ToolCalls)
			continue
		}
		for j := range a.ToolCalls {
			if a.ToolCalls[j] != b.ToolCalls[j] {
				t.Errorf("message %d call %d differs: %+v vs %+v", i, j, a.ToolCalls[j], b.ToolCalls[j])
			}
		}
	}
}

func TestUserQuestionBudgetsRestoreNeverFresh(t *testing.T) {
	row := aichattools.NewBudget(3)
	row.Spend(2)
	page := aichattools.NewPageContentBudget(1000, 2)
	if !page.TryRegisterPage("paid-page") {
		t.Fatal("register paid page")
	}
	page.SpendBytes(400)
	web := aichattools.NewWebBudget(3, 3)
	if err := web.SpendSearch(); err != nil {
		t.Fatal(err)
	}
	suggest := aichattools.NewSuggestBudget(4, 40)
	if err := suggest.SpendRequests(30); err != nil {
		t.Fatal(err)
	}
	skill := aichattools.NewSkillBudget(100)
	skill.Spend(60)
	snapshot := snapshotUserQuestionBudgets(aichattools.Scope{
		RowBudget: row, PageContentBudget: page, WebBudget: web, SuggestBudget: suggest, SkillsBudget: skill,
	})
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded userQuestionBudgets
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	restoredRow, restoredPage, restoredWeb, restoredSuggest, restoredSkill := decoded.restore()
	if got := restoredRow.Remaining(); got != 1 {
		t.Errorf("row budget = %d, want 1", got)
	}
	if got := restoredPage.RemainingBytes(); got != 600 {
		t.Errorf("page bytes = %d, want 600", got)
	}
	if !restoredPage.TryRegisterPage("paid-page") {
		t.Error("paid page SeenKeys lost: re-read of a paid page must stay free")
	}
	if !restoredPage.TryRegisterPage("fresh-page") {
		t.Error("the one remaining unique slot must survive the restore")
	}
	if restoredPage.TryRegisterPage("another-fresh-page") {
		t.Error("restored page budget handed out a fresh unique slot")
	}
	if searches, fetches := restoredWeb.Remaining(); searches != 2 || fetches != 3 {
		t.Errorf("web = %d/%d, want 2/3", searches, fetches)
	}
	if calls, requests := restoredSuggest.Remaining(); calls != 4 || requests != 10 {
		t.Errorf("suggest = %d/%d, want 4/10", calls, requests)
	}
	if got := restoredSkill.Remaining(); got != 40 {
		t.Errorf("skill = %d, want 40", got)
	}
}

func TestDeferredUserQuestionResultNamesCall(t *testing.T) {
	call := ai.ToolCall{ID: "call-9", Name: "update_business_profile", Args: `{"x":1}`}
	status, result := deferredUserQuestionResult(call)
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	for _, want := range []string{"call-9", "update_business_profile", "not executed"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("deferred result missing %q: %q", want, result.Content)
		}
	}
	if result.Summary == "" {
		t.Error("empty deferred summary")
	}
}

func TestInvalidUserQuestionArgsResult(t *testing.T) {
	call := ai.ToolCall{ID: "call-1", Name: aichattools.AskUserQuestionName, Args: `{"questions":[]}`}
	status, result := invalidUserQuestionArgsResult(call, errors.New("boom"))
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(result.Content, "boom") || !strings.Contains(result.Content, "call-1") {
		t.Errorf("invalid-args result missing reason: %q", result.Content)
	}
}
