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
		MCPToolOptions{ConnectionID: testConnectionID, Service: MCPServiceCustom})
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

// TestDiagnosticsOmitOnlyInvalidAdvertisements proves every valid advertised
// name is served on every service: file, SQL, shell and batch names carry no
// omission reason, and only a blank name or a missing schema is invalid_tool.
func TestDiagnosticsOmitOnlyInvalidAdvertisements(t *testing.T) {
	specs := []MCPToolDef{
		{Name: "run_sql", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_file", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "exec_shell", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "batch_update_content", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "no_schema", Description: "remote"},
		{Name: "kept", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	for _, service := range []string{MCPServiceWordPress, MCPServiceCustom} {
		tools, omitted := BuildMCPToolsWithDiagnostics(specs, &fakeMCPSession{},
			MCPToolOptions{ConnectionID: testConnectionID, Service: service})
		if len(tools) != 5 {
			t.Fatalf("%s served %d tools, want the 5 valid advertisements", service, len(tools))
		}
		byRemote := map[string]MCPOmittedTool{}
		for _, o := range omitted {
			byRemote[o.Remote] = o
		}
		for _, name := range []string{"run_sql", "read_file", "exec_shell", "batch_update_content", "kept"} {
			if _, excluded := byRemote[name]; excluded {
				t.Fatalf("%s omits %q, want it served", service, name)
			}
		}
		if o, ok := byRemote[""]; !ok || o.Reason != MCPOmitInvalidTool {
			t.Fatalf("%s blank-name omission = %+v, want invalid_tool", service, o)
		}
		if o, ok := byRemote["no_schema"]; !ok || o.Reason != MCPOmitInvalidTool {
			t.Fatalf("%s schema-less omission = %+v, want invalid_tool", service, o)
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

// TestEveryServiceServesEveryName keeps every path free of provider
// heuristics: every advertised name is served or fails validation, on
// wordpress and custom alike.
func TestEveryServiceServesEveryName(t *testing.T) {
	specs := testToolSpecs("run_sql", "exec_shell", "describe_tables", "batch_update_content", "write_file")
	for _, service := range []string{MCPServiceWordPress, MCPServiceCustom} {
		tools, omitted := BuildMCPToolsWithDiagnostics(specs, &fakeMCPSession{},
			MCPToolOptions{ConnectionID: testConnectionID, Service: service})
		if len(tools) != len(specs) {
			t.Fatalf("%s served %d of %d tools", service, len(tools), len(specs))
		}
		if len(omitted) != 0 {
			t.Fatalf("%s omitted %+v, want none", service, omitted)
		}
	}
}
