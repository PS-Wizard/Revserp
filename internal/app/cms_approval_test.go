package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestMCPApprovalJSONShape(t *testing.T) {
	created := pgtype.Timestamptz{}
	_ = created.Scan(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	connectionID := pgtype.UUID{}
	_ = connectionID.Scan("11111111-1111-1111-1111-111111111111")
	pending := newMCPApprovalJSON(mcpApprovalData{
		ToolCallID: "call-1", ToolName: "mcp_abc", Provider: "wordpress", Service: "wordpress",
		ConnectionID: connectionID, ConnectionName: "WordPress", RemoteToolName: "update_content",
		Target: "update_content #1", BeforeText: "", AfterText: `{"title":"x"}`,
		Snapshot: []byte(`{}`), ProposedArgs: []byte(`{"title":"x"}`),
		Status: "pending", CreatedAt: created,
	})
	raw, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "turn_id", "tool_call_id", "tool_name", "service", "connection_id", "connection_name", "remote_tool_name", "target", "before", "after", "status", "created_at", "proposed_args"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("approval missing %q: %s", key, raw)
		}
	}
	if decoded["service"] != "wordpress" || decoded["remote_tool_name"] != "update_content" {
		t.Errorf("approval identity = %s", raw)
	}
	if _, ok := decoded["decided_at"]; ok {
		t.Errorf("pending approval exposes decided_at: %s", raw)
	}
	if _, ok := decoded["schema_digest"]; ok {
		t.Errorf("approval leaks schema digest: %s", raw)
	}
}

func TestMCPApprovalJSONKeepsHistoryWithoutConnection(t *testing.T) {
	created := pgtype.Timestamptz{}
	_ = created.Scan(time.Now())
	legacy := newMCPApprovalJSON(mcpApprovalData{
		ToolCallID: "call-0", ToolName: "cms__update_record", Provider: "rune",
		Target: "posts", ProposedArgs: []byte(`{}`), Status: "rejected", CreatedAt: created,
	})
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "connection_id") {
		t.Errorf("legacy approval invents a connection: %s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["provider"] != "rune" {
		t.Errorf("legacy provider lost: %s", raw)
	}
}

func TestAITurnResponseCarriesCursorAndApprovals(t *testing.T) {
	created := pgtype.Timestamptz{}
	_ = created.Scan(time.Now())
	response := aiTurnResponse{ID: "t", Status: "running", Messages: []aiMessageResponse{}, ToolCalls: []aiToolCallResponse{}, Approvals: []mcpApprovalJSON{}, EventCursor: 42}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["event_cursor"] != float64(42) {
		t.Errorf("event_cursor = %v, want 42: the frontend seeds its subscription from it", decoded["event_cursor"])
	}
	approvals, ok := decoded["approvals"].([]any)
	if !ok {
		t.Fatalf("approvals missing or not an array: %s", raw)
	}
	if len(approvals) != 0 {
		t.Errorf("approvals = %v, want empty array not null", approvals)
	}
}

func TestAITurnStreamCloseSemantics(t *testing.T) {
	// Terminal statuses close the stream; waiting closes it on the CURRENT
	// status only so a resumed turn's replay is never cut short.
	for status, want := range map[string]bool{
		"completed": true, "stopped": true, "failed": true,
		"waiting_for_user": true,
		"queued":           false, "running": false, "waiting": false,
	} {
		if got := aiTurnStreamClosesOnStatus(status); got != want {
			t.Errorf("aiTurnStreamClosesOnStatus(%q) = %v, want %v", status, got, want)
		}
	}
	// The historical waiting event itself is never terminal: replay on a
	// resumed turn must stream past it to the post-resume events.
	for _, eventType := range []string{"approval_required", "approval_decided", "waiting_for_user", "tool_call", "tool_result", "phase", "text_delta"} {
		if aiTurnEventIsTerminal(eventType) {
			t.Errorf("aiTurnEventIsTerminal(%q) = true, want false", eventType)
		}
	}
}
