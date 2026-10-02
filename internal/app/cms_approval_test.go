package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestParseCMSProvider(t *testing.T) {
	for _, value := range []string{"rune", "wordpress", " Rune ", "WORDPRESS"} {
		if _, ok := parseCMSProvider(value); !ok {
			t.Errorf("provider %q rejected", value)
		}
	}
	for _, value := range []string{"", "drupal", "rune ", " cms"} {
		if value == "rune " {
			continue // trailing space trims to a valid provider
		}
		if _, ok := parseCMSProvider(value); ok {
			t.Errorf("provider %q accepted", value)
		}
	}
	if _, ok := parseCMSProvider("rune "); !ok {
		t.Error("padded rune rejected")
	}
}

func TestCMSStatusResponseShape(t *testing.T) {
	tools := []runeToolResponse{
		{Name: "cms__create_record", Description: "Create."},
		{Name: "cms__list_records", Description: "List."},
		{Name: "wp__update_content", Description: "Update."},
		{Name: "wp__not_a_tool", Description: "Unknown."},
	}
	runeResp := newCMSStatusResponse(true, CMSProviderRune, "https://rune.example/mcp", time.Now(), tools[:2])
	if runeResp.Provider != "rune" || !runeResp.Connected {
		t.Fatalf("rune status = %+v", runeResp)
	}
	if len(runeResp.Tools) != 2 || runeResp.Tools[0].Group != "rune" {
		t.Fatalf("rune tools = %+v", runeResp.Tools)
	}
	if !runeResp.Tools[0].Write || runeResp.Tools[1].Write {
		t.Errorf("rune write flags = %+v, want create=true list=false", runeResp.Tools)
	}
	// WordPress catalogued entries take the reviewed metadata; a discovered
	// tool the catalogue does not describe is reported, never dropped.
	wpResp := newCMSStatusResponse(true, CMSProviderWordPress, "https://wp.example", time.Now(), tools[2:])
	if len(wpResp.Tools) != 2 {
		t.Fatalf("wp tools = %+v, want the unknown tool kept", wpResp.Tools)
	}
	if !wpResp.Tools[0].Write || wpResp.Tools[0].Group == "" {
		t.Errorf("wp catalogued entry = %+v", wpResp.Tools[0])
	}
	if wpResp.Tools[1].Name != "wp__not_a_tool" || wpResp.Tools[1].Description != "Unknown." || wpResp.Tools[1].Write {
		t.Errorf("wp unknown entry = %+v, want the discovered metadata", wpResp.Tools[1])
	}
	raw, err := json.Marshal(newCMSStatusResponse(false, "", "", time.Time{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tools":[]`) || strings.Contains(string(raw), `"provider"`) {
		t.Errorf("disconnected body = %s", raw)
	}
	for _, needle := range []string{"token", "cipher", "secret", "bearer"} {
		if strings.Contains(strings.ToLower(string(raw)), needle) {
			t.Errorf("status body mentions %q: %s", needle, raw)
		}
	}
}

// TestCMSStatusKeepsUnknownDiscoveredToolsAndGroups covers the discovery
// contract: every accepted tool reaches the project status with its name,
// description and optional server group, including names no catalogue has.
func TestCMSStatusKeepsUnknownDiscoveredToolsAndGroups(t *testing.T) {
	for _, tc := range []struct {
		provider CMSProvider
		name     string
	}{
		{CMSProviderRune, "cms__brand_new_thing"},
		{CMSProviderWordPress, "wp__brand_new_thing"},
	} {
		status := newCMSStatusResponse(true, tc.provider, "https://cms.example", time.Now(), []runeToolResponse{
			{Name: tc.name, Description: "Server supplied.", Group: "experimental"},
		})
		if len(status.Tools) != 1 {
			t.Fatalf("%s: unknown tool dropped: %+v", tc.provider, status.Tools)
		}
		entry := status.Tools[0]
		if entry.Name != tc.name || entry.Description != "Server supplied." || entry.Group != "experimental" || entry.Write {
			t.Errorf("%s: unknown entry = %+v, want the discovered metadata", tc.provider, entry)
		}
	}
}

func TestNormalizeRuneToolsKeepsGroupAndUnknownNames(t *testing.T) {
	raw, response, err := normalizeRuneTools([]RuneTool{
		{Name: "brand_new_thing", Description: "Server supplied.", Group: "experimental", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(response) != 1 || response[0].Name != "brand_new_thing" || response[0].Group != "experimental" {
		t.Fatalf("response = %+v", response)
	}
	stored, err := parseRuneStoredTools(raw)
	if err != nil {
		t.Fatalf("parse stored: %v", err)
	}
	if len(stored) != 1 || stored[0].Group != "experimental" || stored[0].Description != "Server supplied." {
		t.Fatalf("stored round trip lost the group: %+v", stored)
	}
	if _, _, err := normalizeRuneTools([]RuneTool{{Name: "g", Group: strings.Repeat("x", runeMaxToolGroupLen+1), InputSchema: json.RawMessage(`{}`)}}); runeErrorCode(err) != "invalid_tools" {
		t.Errorf("oversized group accepted")
	}
}

func TestCMSConnectFailureWordPressStyle(t *testing.T) {
	for code, message := range map[string]string{
		"invalid_endpoint":      "invalid wordpress endpoint",
		"invalid_token":         "invalid wordpress credentials",
		"unauthorized":          "invalid wordpress credentials",
		"unsupported_transport": "unsupported wordpress endpoint",
		"unreachable":           "wordpress endpoint unreachable",
		"timeout":               "wordpress endpoint timed out",
		"too_large":             "wordpress response too large",
		"invalid_tools":         "invalid wordpress tools response",
		"bogus":                 "failed to reach wordpress endpoint",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		cmsConnectFailure(rec, req, runeCodedError{code: code}, CMSProviderWordPress)
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("code %q: decode: %v", code, err)
		}
		if body["error"] != message {
			t.Errorf("code %q: message = %q, want %q", code, body["error"], message)
		}
	}
}

func TestCMSApprovalJSONShape(t *testing.T) {
	created := pgtype.Timestamptz{}
	_ = created.Scan(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	pending := newCMSApprovalJSON(cmsApprovalData{
		ToolCallID: "call-1", ToolName: "cms__create_record", Provider: "rune",
		Target: "posts", BeforeText: "", AfterText: `{"title":"x"}`,
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
	for _, key := range []string{"id", "turn_id", "tool_call_id", "tool_name", "provider", "target", "before", "after", "status", "created_at", "proposed_args"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("approval missing %q: %s", key, raw)
		}
	}
	if _, ok := decoded["decided_at"]; ok {
		t.Errorf("pending approval exposes decided_at: %s", raw)
	}
	if decoded["before"] != "" {
		t.Errorf("before is not plain text: %s", raw)
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
