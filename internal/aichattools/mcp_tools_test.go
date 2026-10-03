package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeMCPSession records what the builders dispatch and answers from onCall.
type fakeMCPSession struct {
	calls  []string
	args   []json.RawMessage
	onCall func(name string, args json.RawMessage) (MCPResult, error)
}

func (f *fakeMCPSession) Tools() []MCPToolDef { return nil }

func (f *fakeMCPSession) Call(_ context.Context, name string, args json.RawMessage) (MCPResult, error) {
	f.calls = append(f.calls, name)
	f.args = append(f.args, args)
	if f.onCall != nil {
		return f.onCall(name, args)
	}
	return MCPResult{Content: `{"ok":true}`}, nil
}

func (f *fakeMCPSession) Close() error { return nil }

const testConnectionID = "11111111-2222-3333-4444-555555555555"

func TestMCPModelToolNameIsStableBoundedAndPerConnection(t *testing.T) {
	first := MCPModelToolName(testConnectionID, "update_content")
	if first != MCPModelToolName(testConnectionID, "update_content") {
		t.Fatal("alias is not stable for the same connection and tool")
	}
	if len(first) > 64 || len(first) != mcpAliasLen {
		t.Fatalf("alias %q is %d characters", first, len(first))
	}
	if !IsMCPModelToolName(first) {
		t.Fatalf("alias %q is not recognized as a canonical MCP alias", first)
	}
	if strings.Contains(first, "update_content") {
		t.Fatal("alias leaks the remote tool name")
	}
	sameNameOtherConnection := MCPModelToolName("99999999-2222-3333-4444-555555555555", "update_content")
	if sameNameOtherConnection == first {
		t.Fatal("two connections sharing a remote tool name must not share an alias")
	}
	otherName := MCPModelToolName(testConnectionID, "publish_content")
	if otherName == first {
		t.Fatal("two remote names on one connection must not share an alias")
	}
	renamed := MCPModelToolName(testConnectionID, "update_content_v2")
	if renamed == first {
		t.Fatal("a renamed remote tool must produce a new alias so it starts at Ask again")
	}
	if notAlias := MCPModelToolName("not-a-uuid", "update_content"); notAlias != "" {
		t.Fatalf("a connection id that is not a UUID must yield no alias, got %q", notAlias)
	}
	if unservable := BuildMCPTools([]MCPToolDef{{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}}, &fakeMCPSession{}, MCPToolOptions{ConnectionID: "not-a-uuid", Service: MCPServiceCustom}); len(unservable) != 0 {
		t.Fatalf("a connection with no usable identity served %d tools", len(unservable))
	}
	for _, native := range []string{"read_issues", "mcp__update_content", "mcp_" + strings.Repeat("z", 32) + "_" + strings.Repeat("0", 16)} {
		if IsMCPModelToolName(native) {
			t.Fatalf("native or malformed name %q was accepted as an MCP alias", native)
		}
	}
}

func TestBuildMCPToolsServesAliasesNotRemoteNames(t *testing.T) {
	session := &fakeMCPSession{}
	specs := []MCPToolDef{
		{Name: "update_content", Description: "remote text", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)},
		{Name: "update_content", Description: "duplicate advertisement", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "search_without_schema"},
	}
	tools := BuildMCPTools(specs, session, MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
	if len(tools) != 3 {
		t.Fatalf("got %d tools, want the 3 advertised with a live schema", len(tools))
	}
	if tools[0].Def.Name != tools[1].Def.Name {
		t.Fatal("a repeated advertisement must produce the repeated alias Registry.Add rejects")
	}
	registry := NewFilteredRegistry(nil)
	collisions := 0
	for _, tool := range tools {
		if tool.Def.Name != MCPModelToolName(testConnectionID, tool.Def.Label) {
			t.Fatalf("tool name %q is not the canonical alias of remote tool %q", tool.Def.Name, tool.Def.Label)
		}
		if err := registry.Add(tool); err != nil {
			collisions++
		}
		if !strings.Contains(tool.Def.Description, mcpUnreviewedToolNote) {
			t.Fatalf("undescribed tool %q lost its untrusted-metadata note: %q", tool.Def.Name, tool.Def.Description)
		}
		if len(tool.Def.Schema) == 0 {
			t.Fatalf("tool %q has no schema", tool.Def.Name)
		}
	}
	if collisions != 1 {
		t.Fatalf("registry rejected %d tools, want the one repeated alias rejected loudly", collisions)
	}
}

func TestBuildMCPToolsServesEveryAdvertisedName(t *testing.T) {
	specs := testToolSpecs("run_sql", "read_file", "write_file", "exec_shell", "batch_update_content", "describe_tables", "create_child_theme", "search")
	for _, service := range []string{MCPServiceWordPress, MCPServiceCustom} {
		tools, omitted := BuildMCPToolsWithDiagnostics(specs, &fakeMCPSession{}, MCPToolOptions{ConnectionID: testConnectionID, Service: service})
		if len(tools) != len(specs) {
			t.Fatalf("%s served %d tools, want all %d: no name is excluded", service, len(tools), len(specs))
		}
		if len(omitted) != 0 {
			t.Fatalf("%s omitted %+v, want none", service, omitted)
		}
		for _, tool := range tools {
			if tool.Def.Name != MCPModelToolName(testConnectionID, tool.Def.Label) {
				t.Fatalf("tool name %q is not the canonical alias of remote tool %q", tool.Def.Name, tool.Def.Label)
			}
		}
	}
}

func TestMCPToolGuardsDispatchExactRemoteName(t *testing.T) {
	session := &fakeMCPSession{}
	tools := BuildMCPTools(testToolSpecs("search"), session, MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
	if _, err := tools[0].Execute(context.Background(), json.RawMessage(`{"q":"x"}`), Scope{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(session.calls) != 1 || session.calls[0] != "search" {
		t.Fatalf("dispatched %v, want the exact remote name", session.calls)
	}

	stale := &fakeMCPSession{}
	guarded := BuildMCPTools(testToolSpecs("search"), stale, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceCustom,
		Guard:        func(context.Context) error { return errors.New("membership gone") },
	})
	result, err := guarded[0].Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stale.calls) != 0 {
		t.Fatal("a guard failure must dispatch nothing")
	}
	if !strings.Contains(result.Content, "not performed") {
		t.Fatalf("guard failure result %q does not say the action was not performed", result.Content)
	}

	none := BuildMCPTools(testToolSpecs("search"), nil, MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
	result, err = none[0].Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil || !strings.Contains(result.Content, "not available") {
		t.Fatalf("a missing connection must stay an ordinary unavailable result, got %v %q", err, result.Content)
	}
}

func TestUnknownCallOutcomeReportsTruthfullyWithoutLockout(t *testing.T) {
	calls := 0
	session := &fakeMCPSession{onCall: func(string, json.RawMessage) (MCPResult, error) {
		calls++
		if calls == 1 {
			return MCPResult{}, errors.New("connection reset")
		}
		return MCPResult{Content: `{"ok":true}`}, nil
	}}
	tools := BuildMCPTools(testToolSpecs("mystery_write", "mystery_read"), session, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceCustom,
	})
	first, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(first.Content, "outcome is unknown") || !strings.Contains(first.Content, "may already have applied") {
		t.Fatalf("unknown outcome result %q does not report the ambiguity truthfully", first.Content)
	}
	if !strings.Contains(first.Content, "Check the current remote state") {
		t.Fatalf("unknown outcome result %q gives no remote-state guidance", first.Content)
	}
	for _, command := range []string{"Do not retry", "do not retry", "no further call"} {
		if strings.Contains(first.Content, command) {
			t.Fatalf("unknown outcome result %q bans a later call", first.Content)
		}
	}
	second, err := tools[1].Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(session.calls) != 2 {
		t.Fatalf("dispatched %d calls, want the later permitted call to run normally", len(session.calls))
	}
	if !strings.Contains(second.Content, `{"ok":true}`) {
		t.Fatalf("later call result %q is not the live result", second.Content)
	}
}

func TestWordPressUnknownOutcomeDoesNotBlockLaterCalls(t *testing.T) {
	session := &fakeMCPSession{onCall: func(name string, _ json.RawMessage) (MCPResult, error) {
		if name == "update_content" {
			return MCPResult{}, errors.New("connection reset")
		}
		return MCPResult{Content: "page title: Hello"}, nil
	}}
	tools := BuildMCPTools(testToolSpecs("update_content", "get_content"), session, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceWordPress,
	})
	if _, err := tools[0].Execute(context.Background(), json.RawMessage(`{"id":"1"}`), Scope{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	read, err := tools[1].Execute(context.Background(), json.RawMessage(`{"id":"1"}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(session.calls) != 2 {
		t.Fatalf("dispatched %d calls, want the later call to run under normal checks", len(session.calls))
	}
	if !strings.Contains(read.Content, "page title") {
		t.Fatalf("later call result %q is not the live result", read.Content)
	}
}

func TestWordPressResultSemanticsStayTruthful(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		wantSummary string
		wantAbsent  string
	}{
		{name: "queued", content: `{"status":"queued_for_approval"}`, wantSummary: "queued for the site's own approval, not applied", wantAbsent: wordPressAppliedNote},
		{name: "failed", content: `{"ok":false,"error":"permission denied"}`, wantSummary: "permission denied"},
		{name: "partial", content: `{"partial":true}`, wantSummary: "partly applied", wantAbsent: wordPressAppliedNote},
		{name: "undo incomplete", content: `{"undone":false}`, wantSummary: "undo incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeMCPSession{onCall: func(string, json.RawMessage) (MCPResult, error) {
				return MCPResult{Content: tc.content}, nil
			}}
			tools := BuildMCPTools(testToolSpecs("update_content"), session, MCPToolOptions{
				ConnectionID: testConnectionID,
				Service:      MCPServiceWordPress,
			})
			result, err := tools[0].Execute(context.Background(), json.RawMessage(`{"id":"1"}`), Scope{})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(result.Summary, tc.wantSummary) {
				t.Fatalf("summary %q does not contain %q", result.Summary, tc.wantSummary)
			}
			if tc.wantAbsent != "" && strings.Contains(result.Content, tc.wantAbsent) {
				t.Fatalf("result claims %q: %q", tc.wantAbsent, result.Content)
			}
		})
	}
	applied := &fakeMCPSession{onCall: func(string, json.RawMessage) (MCPResult, error) {
		return MCPResult{Content: `{"ok":true}`}, nil
	}}
	tools := BuildMCPTools(testToolSpecs("publish_content"), applied, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceWordPress,
	})
	result, err := tools[0].Execute(context.Background(), json.RawMessage(`{"content":"hello"}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, wordPressAppliedNote) {
		t.Fatalf("applied write result %q does not say what is live", result.Content)
	}
}

func TestMCPToolErrorResultIsNotAnUnknownOutcome(t *testing.T) {
	session := &fakeMCPSession{onCall: func(string, json.RawMessage) (MCPResult, error) {
		return MCPResult{Content: "no such post", IsError: true}, nil
	}}
	tools := BuildMCPTools(testToolSpecs("mystery_write"), session, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceCustom,
	})
	result, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, "no such post") {
		t.Fatalf("tool error %q is not surfaced", result.Content)
	}
	if strings.Contains(result.Content, "outcome is unknown") {
		t.Fatalf("tool-reported failure %q is misreported as an unknown outcome", result.Content)
	}
	again, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(session.calls) != 2 || !strings.Contains(again.Content, "no such post") {
		t.Fatalf("later call = %d dispatches %q, want a normal second failure", len(session.calls), again.Content)
	}
}

// testToolSpecs builds advertised tools the way the transport delivers them:
// every accepted tool carries a live object input schema.
func testToolSpecs(names ...string) []MCPToolDef {
	specs := make([]MCPToolDef, 0, len(names))
	for _, name := range names {
		specs = append(specs, MCPToolDef{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return specs
}
