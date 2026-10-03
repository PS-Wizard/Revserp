package aichattools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGenericDescriptionKeepsServerTextAndNamesRemote proves the generic
// path preserves what the server advertised: the exact remote name, the
// bounded server description, and a truthful untrusted-metadata note. It
// must never claim nothing is known when the server supplied a description.
func TestGenericDescriptionKeepsServerTextAndNamesRemote(t *testing.T) {
	session := &fakeMCPSession{}
	tools := BuildMCPTools([]MCPToolDef{{
		Name:        "delete_record",
		Description: "Permanently delete one record by id from a base collection.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}, session, MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
	if len(tools) != 1 {
		t.Fatalf("served %d tools, want 1", len(tools))
	}
	description := tools[0].Def.Description
	if !strings.Contains(description, `"delete_record"`) {
		t.Fatalf("description does not name the exact remote tool: %q", description)
	}
	if !strings.Contains(description, "Permanently delete one record") {
		t.Fatalf("description dropped the bounded server text: %q", description)
	}
	if !strings.Contains(description, mcpUnreviewedToolNote) {
		t.Fatalf("description lost the untrusted-metadata note: %q", description)
	}
	if strings.Contains(description, "nothing about what it changes is known") {
		t.Fatalf("description falsely claims nothing is known despite server text: %q", description)
	}
}

// TestGenericDescriptionWithoutServerTextStaysTruthful covers a server that
// advertises no description: the gap is stated plainly next to the exact
// remote name, without inventing behavior.
func TestGenericDescriptionWithoutServerTextStaysTruthful(t *testing.T) {
	tools := BuildMCPTools(testToolSpecs("mystery_tool"), &fakeMCPSession{},
		MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
	if len(tools) != 1 {
		t.Fatalf("served %d tools, want 1", len(tools))
	}
	description := tools[0].Def.Description
	if !strings.Contains(description, `"mystery_tool"`) {
		t.Fatalf("description does not name the exact remote tool: %q", description)
	}
	if !strings.Contains(description, "No description supplied by the MCP server.") {
		t.Fatalf("missing server text is not stated plainly: %q", description)
	}
}

// TestGenericResultMarksUntrustedOutput proves an undescribed tool's result
// is reported as untrusted data with no invented effect.
func TestGenericResultMarksUntrustedOutput(t *testing.T) {
	session := &fakeMCPSession{}
	tools := BuildMCPTools(testToolSpecs("mystery_tool"), session,
		MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom, Writes: &MCPWriteState{}})
	result, err := tools[0].Execute(t.Context(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, `{"ok":true}`) {
		t.Fatalf("result lost the server payload: %q", result.Content)
	}
	if !strings.Contains(result.Content, mcpUnreviewedResultNote) {
		t.Fatalf("result lost its untrusted-data note: %q", result.Content)
	}
}

// TestDiagnosticsSeparateOmissionReasons proves each unsaved advertisement
// carries its own stable reason: a deliberate WordPress safety exclusion is
// platform_restricted with its truthful detail, never an alias collision.
func TestDiagnosticsSeparateOmissionReasons(t *testing.T) {
	specs := []MCPToolDef{
		{Name: "run_sql", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "no_schema", Description: "remote"},
		{Name: "kept", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	tools, omitted := BuildMCPToolsWithDiagnostics(specs, &fakeMCPSession{},
		MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceWordPress})
	if len(tools) != 1 || tools[0].Def.Label != "kept" {
		t.Fatalf("served %d tools, want only the unrestricted one", len(tools))
	}
	byRemote := map[string]MCPOmittedTool{}
	for _, o := range omitted {
		byRemote[o.Remote] = o
	}
	restricted, ok := byRemote["run_sql"]
	if !ok || restricted.Reason != MCPOmitPlatformRestricted || restricted.Detail == "" {
		t.Fatalf("run_sql omission = %+v, want platform_restricted with detail", restricted)
	}
	if reason, _ := WordPressToolRestriction("run_sql"); reason != restricted.Detail {
		t.Fatalf("omission detail %q is not the reviewed restriction reason", restricted.Detail)
	}
	if o, ok := byRemote[""]; !ok || o.Reason != MCPOmitInvalidTool {
		t.Fatalf("blank-name omission = %+v, want invalid_tool", o)
	}
	if o, ok := byRemote["no_schema"]; !ok || o.Reason != MCPOmitInvalidTool {
		t.Fatalf("schema-less omission = %+v, want invalid_tool", o)
	}
	for _, o := range omitted {
		if o.Reason == MCPOmitPlatformRestricted && o.Remote != "run_sql" {
			t.Fatalf("omission %+v mislabels a non-excluded tool as platform-restricted", o)
		}
	}
}

// TestDiagnosticsAliasRejected keeps the no-identity case loud: a
// connection id with no usable identity serves nothing and says why.
func TestDiagnosticsAliasRejected(t *testing.T) {
	tools, omitted := BuildMCPToolsWithDiagnostics(testToolSpecs("search"), &fakeMCPSession{},
		MCPToolOptions{ConnectionID: "not-a-uuid", Service: MCPServiceCustom})
	if len(tools) != 0 {
		t.Fatalf("served %d tools without an identity", len(tools))
	}
	if len(omitted) != 1 || omitted[0].Reason != MCPOmitAliasRejected || omitted[0].Remote != "search" {
		t.Fatalf("omitted = %+v, want one alias_rejected entry for search", omitted)
	}
}

// TestCustomNeverRestrictedByName keeps the generic path free of provider
// heuristics: every advertised custom name is served or fails validation,
// never excluded as a platform restriction.
func TestCustomNeverRestrictedByName(t *testing.T) {
	specs := testToolSpecs("run_sql", "exec_shell", "describe_tables", "batch_update_content")
	tools, omitted := BuildMCPToolsWithDiagnostics(specs, &fakeMCPSession{},
		MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
	if len(tools) != len(specs) {
		t.Fatalf("custom served %d of %d tools", len(tools), len(specs))
	}
	for _, o := range omitted {
		if o.Reason == MCPOmitPlatformRestricted {
			t.Fatalf("custom omission %+v applies a platform restriction by name", o)
		}
	}
}
