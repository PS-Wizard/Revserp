package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fakeRuneSession is a local fake connector: it never touches the network.
type fakeRuneSession struct {
	mu       sync.Mutex
	tools    []RuneToolDef
	calls    int
	callsFor map[string]int
	onCall   func(name string, args json.RawMessage) (RuneCallResult, error)
	closed   bool
}

func (f *fakeRuneSession) Tools() []RuneToolDef { return f.tools }

func (f *fakeRuneSession) Call(_ context.Context, name string, args json.RawMessage) (RuneCallResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.callsFor == nil {
		f.callsFor = map[string]int{}
	}
	f.callsFor[name]++
	if f.onCall != nil {
		return f.onCall(name, args)
	}
	return RuneCallResult{Content: `{"ok":true}`}, nil
}

func (f *fakeRuneSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeRuneSession) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func liveRuneSpecs() []RuneToolDef {
	return []RuneToolDef{
		{Name: "list_collections", Description: "REMOTE DESCRIPTION MUST NOT BE USED", InputSchema: json.RawMessage(`{"type":"object","properties":{"only_live":{"type":"string"}}}`)},
		{Name: "list_records", Description: "REMOTE", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_record", Description: "REMOTE", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "create_record", Description: "REMOTE", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "update_record", Description: "REMOTE", InputSchema: nil},
		{Name: "bogus_tool", Description: "REMOTE", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "list_records", Description: "DUPLICATE", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func TestRuneStaticCatalog(t *testing.T) {
	names := RuneStaticNames()
	if len(names) != 6 {
		t.Fatalf("RuneStaticNames() = %v, want six tools", names)
	}
	byName := map[string]Def{}
	for _, def := range CatalogDefs() {
		byName[def.Name] = def
	}
	for _, name := range names {
		def, ok := byName[name]
		if !ok {
			t.Fatalf("catalog missing %q", name)
		}
		if def.Feature != RuneFeature {
			t.Errorf("%q feature = %q, want %q", name, def.Feature, RuneFeature)
		}
		if strings.TrimSpace(def.Label) == "" || strings.TrimSpace(def.Description) == "" || len(def.Schema) == 0 {
			t.Errorf("%q has an incomplete static def: %+v", name, def)
		}
	}
	for _, write := range []string{"cms__create_record", "cms__update_record"} {
		def := byName[write]
		if !strings.Contains(def.Description, "explicitly") {
			t.Errorf("%q description must gate on an explicit user request, got %q", write, def.Description)
		}
		if !strings.Contains(def.Description, "published site is unchanged until an explicit static build") {
			t.Errorf("%q description must state the published site is unchanged until an explicit static build, got %q", write, def.Description)
		}
	}
	if feature := ToolFeatures()["cms__list_records"]; feature != RuneFeature {
		t.Errorf("ToolFeatures()[cms__list_records] = %q, want %q", feature, RuneFeature)
	}
	if _, ok := NewRegistry().Get("cms__list_records"); ok {
		t.Error("default registry must not serve cms__ tools without a live session")
	}
}

func TestRegistryAdd(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Add(Tool{Def: Def{}}); err == nil {
		t.Error("Add(empty name) = nil, want an error")
	}
	dup := registry.Defs()[0]
	if err := registry.Add(Tool{Def: dup}); err == nil {
		t.Errorf("Add(%q duplicate) = nil, want an error", dup.Name)
	}
	extra := Tool{Def: Def{Name: "cms__list_records", Description: "x", Schema: json.RawMessage(`{"type":"object"}`)}}
	if err := registry.Add(extra); err != nil {
		t.Fatalf("Add(cms__list_records) = %v, want nil", err)
	}
	if _, ok := registry.Get("cms__list_records"); !ok {
		t.Error("added tool is not retrievable")
	}
}

func TestNewFilteredRegistryBlocksDisabled(t *testing.T) {
	registry := NewFilteredRegistry([]string{"read_issues"})
	if _, ok := registry.Get("read_issues"); ok {
		t.Error("filtered registry still serves a disabled tool: the model could run it by guessing its name")
	}
	if _, ok := registry.Get("get_score_summary"); !ok {
		t.Error("filtered registry dropped an enabled tool")
	}
	if got, want := len(registry.Defs()), len(NewRegistry().Defs())-1; got != want {
		t.Errorf("filtered defs = %d, want %d", got, want)
	}
}

func TestBuildRuneToolsUsesLiveSchemasNotRemoteText(t *testing.T) {
	session := &fakeRuneSession{}
	writes := &RuneWriteState{}
	tools := BuildRuneTools(liveRuneSpecs(), session, nil, writes)
	if len(tools) != 6 {
		t.Fatalf("built %d tools, want 6 (the duplicate dropped, the unknown kept)", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Def.Name, RuneToolPrefix) {
			t.Errorf("tool %q is not namespaced", tool.Def.Name)
		}
		if tool.Def.Name == "cms__bogus_tool" {
			// An unreviewed tool has no local description, so it carries the
			// bounded live text in the definition only.
			if tool.Def.Description != liveToolDescription("REMOTE") {
				t.Errorf("unknown tool description = %q", tool.Def.Description)
			}
		} else if strings.Contains(tool.Def.Description, "REMOTE") {
			t.Errorf("tool %q uses remote description text: %q", tool.Def.Name, tool.Def.Description)
		}
		if tool.Def.Feature != RuneFeature {
			t.Errorf("tool %q feature = %q, want %q", tool.Def.Name, tool.Def.Feature, RuneFeature)
		}
		seen[tool.Def.Name] = true
	}
	for _, want := range []string{"cms__list_collections", "cms__list_records", "cms__create_record", "cms__update_record"} {
		if !seen[want] {
			t.Errorf("missing built tool %q", want)
		}
	}
	var schema map[string]any
	for _, tool := range tools {
		if tool.Def.Name == "cms__list_collections" {
			if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
				t.Fatal(err)
			}
			if _, ok := schema["properties"].(map[string]any)["only_live"]; !ok {
				t.Errorf("cms__list_collections schema is not the live schema: %s", tool.Def.Schema)
			}
		}
	}
}

func executeRune(t *testing.T, tools []Tool, name, args string) (string, Result) {
	t.Helper()
	registry := NewRegistry()
	for _, tool := range tools {
		if err := registry.Add(tool); err != nil {
			t.Fatal(err)
		}
	}
	bound, ok := registry.Get(name)
	if !ok {
		return "failed", Result{Content: "unknown tool", Summary: "unknown tool"}
	}
	result, err := bound.Execute(context.Background(), json.RawMessage(args), Scope{})
	if err != nil {
		return "failed", Result{Content: err.Error(), Summary: "tool execution failed"}
	}
	return normalizeRuneTestResult(name, result)
}

// normalizeRuneTestResult applies the same tool-error mapping the worker
// uses before persisting ai_tool_calls.
func normalizeRuneTestResult(name string, result Result) (string, Result) {
	const prefixMarker = " error:"
	if strings.HasPrefix(result.Content, name+prefixMarker) {
		if result.Summary == "" {
			result.Summary = strings.TrimSpace(strings.TrimPrefix(result.Content, name+prefixMarker))
		}
		return "failed", result
	}
	return "completed", result
}

func TestRuneReadSuccess(t *testing.T) {
	session := &fakeRuneSession{onCall: func(string, json.RawMessage) (RuneCallResult, error) {
		return RuneCallResult{Content: `{"items":[]}`}, nil
	}}
	tools := BuildRuneTools(liveRuneSpecs(), session, nil, &RuneWriteState{})
	status, result := executeRune(t, tools, "cms__list_records", `{"collection":"posts"}`)
	if status != "completed" || result.Content != `{"items":[]}` {
		t.Fatalf("status=%q content=%q", status, result.Content)
	}
}

func TestRuneIsErrorIsKnownFailure(t *testing.T) {
	session := &fakeRuneSession{onCall: func(string, json.RawMessage) (RuneCallResult, error) {
		return RuneCallResult{Content: "no such collection", IsError: true}, nil
	}}
	tools := BuildRuneTools(liveRuneSpecs(), session, nil, &RuneWriteState{})
	status, result := executeRune(t, tools, "cms__read_record", `{"collection":"x","id":"1"}`)
	if status != "failed" {
		t.Fatalf("status=%q, want failed: an isError tool failure must not imply success", status)
	}
	if !strings.HasPrefix(result.Content, "cms__read_record error:") {
		t.Fatalf("isError content = %q, want the tool-error prefix so normalize marks it failed", result.Content)
	}
	if session.totalCalls() != 1 {
		t.Fatalf("calls = %d, want exactly one (no programmatic retry)", session.totalCalls())
	}
}

func TestRuneTransportErrorOnReadIsSafe(t *testing.T) {
	session := &fakeRuneSession{onCall: func(string, json.RawMessage) (RuneCallResult, error) {
		return RuneCallResult{}, errors.New("connection reset")
	}}
	writes := &RuneWriteState{}
	tools := BuildRuneTools(liveRuneSpecs(), session, nil, writes)
	status, result := executeRune(t, tools, "cms__list_records", `{}`)
	if status != "failed" {
		t.Fatalf("status=%q, want failed with a safe message", status)
	}
	if strings.Contains(result.Content, "connection reset") {
		t.Fatalf("transport detail leaked to the model: %q", result.Content)
	}
	if writes.Uncertain() {
		t.Error("a failed read must not mark writes uncertain")
	}
}

func TestRuneUncertainWriteBlocksFurtherWritesNotReads(t *testing.T) {
	session := &fakeRuneSession{onCall: func(name string, _ json.RawMessage) (RuneCallResult, error) {
		if name == "create_record" {
			return RuneCallResult{}, errors.New("timeout after dispatch")
		}
		return RuneCallResult{Content: `{"items":[]}`}, nil
	}}
	writes := &RuneWriteState{}
	tools := BuildRuneTools(liveRuneSpecs(), session, nil, writes)

	status, first := executeRune(t, tools, "cms__create_record", `{"collection":"posts","data":{"title":"t"}}`)
	if status != "failed" {
		t.Fatalf("first write status=%q, want failed: an unknown outcome must not imply success", status)
	}
	if !strings.Contains(first.Content, "unknown") || !strings.Contains(first.Content, "Do not retry") {
		t.Fatalf("unknown-outcome write must say so and forbid retry, got %q", first.Content)
	}
	if !writes.Uncertain() {
		t.Fatal("transport-unknown write must mark the turn uncertain")
	}

	blockedStatus, second := executeRune(t, tools, "cms__update_record", `{"collection":"posts","id":"1","data":{}}`)
	if blockedStatus != "failed" || !strings.Contains(second.Content, "unknown outcome") {
		t.Fatalf("second write must be blocked as failed, got %q/%q", blockedStatus, second.Content)
	}
	status, read := executeRune(t, tools, "cms__list_records", `{}`)
	if status != "completed" || read.Content != `{"items":[]}` {
		t.Fatalf("reads must still inspect the outcome: status=%q content=%q", status, read.Content)
	}
	if got := session.callsFor["create_record"]; got != 1 {
		t.Fatalf("create_record calls = %d, want exactly one (never retried)", got)
	}
	if _, ok := session.callsFor["update_record"]; ok {
		t.Fatal("blocked update_record reached the session")
	}
}

func TestRuneGuardRejectsStaleCalls(t *testing.T) {
	session := &fakeRuneSession{}
	guardErr := errors.New("revision changed")
	tools := BuildRuneTools(liveRuneSpecs(), session, func(context.Context) error { return guardErr }, &RuneWriteState{})
	status, result := executeRune(t, tools, "cms__list_records", `{}`)
	if status != "failed" {
		t.Fatalf("status=%q, want failed with a safe rejection", status)
	}
	if !strings.Contains(result.Content, "no longer available") || !strings.Contains(result.Content, "not performed") {
		t.Fatalf("stale guard content = %q", result.Content)
	}
	if session.totalCalls() != 0 {
		t.Fatal("stale call reached the session")
	}
}

func TestRuneNilSessionStaysNative(t *testing.T) {
	tools := BuildRuneTools(liveRuneSpecs(), nil, nil, &RuneWriteState{})
	status, result := executeRune(t, tools, "cms__list_records", `{}`)
	if status != "failed" {
		t.Fatalf("status=%q, want failed for a disconnected tool", status)
	}
	if !strings.Contains(result.Content, "not connected") {
		t.Fatalf("nil session content = %q, want a not-connected message", result.Content)
	}
}

func TestRuneNames(t *testing.T) {
	// Dynamic names are accepted so admin validation works; only the session
	// registry decides whether a name exists.
	if !IsRuneToolName("cms__read_record") || !IsRuneToolName("cms__brand_new_tool") || IsRuneToolName("read_issues") || IsRuneToolName("cms__bad name") || IsRuneToolName("cms__cms__read_record") {
		t.Error("IsRuneToolName misclassifies names")
	}
	if !IsRuneWriteName("cms__create_record") || !IsRuneWriteName("cms__update_record") || IsRuneWriteName("cms__read_record") || IsRuneWriteName("read_issues") {
		t.Error("IsRuneWriteName misclassifies names")
	}
	if NamespaceRuneName("read_record") != "cms__read_record" {
		t.Error("NamespaceRuneName is wrong")
	}
}

// Remote CMS content must reach the turn byte-identical: no layer below the
// worker's turn-budget cap may slice it, because a raw byte cut can split a
// multibyte rune and persist invalid UTF-8 in ai_tool_calls while a silent
// cut can ship truncated JSON that looks complete.
func TestRuneMultibyteContentPassesThroughIntact(t *testing.T) {
	pad := strings.Repeat("a", (16<<10)-2)
	// Emojis straddle the old 16KiB cut point; a byte slice there would split
	// a rune and produce invalid UTF-8.
	records := strings.Repeat("\"éx\",", 1200)
	content := "{\"items\":[\"" + pad + "🚀🚀🚀\"," + records + "]}"
	if len(content) < 16<<10 {
		t.Fatalf("fixture is %d bytes, want it past the old 16KiB cut", len(content))
	}
	session := &fakeRuneSession{onCall: func(string, json.RawMessage) (RuneCallResult, error) {
		return RuneCallResult{Content: content}, nil
	}}
	tools := BuildRuneTools(liveRuneSpecs(), session, nil, &RuneWriteState{})
	status, result := executeRune(t, tools, "cms__list_records", `{}`)
	if status != "completed" {
		t.Fatalf("status=%q, want completed", status)
	}
	if result.Content != content {
		t.Fatal("CMS content was altered in transit")
	}
	if !utf8.ValidString(result.Content) {
		t.Fatal("CMS content is not valid UTF-8")
	}
}

// A discovered tool with no catalogue entry runs without approval, keeps the
// membership guard, and never claims a static build happened.
func TestRuneUnknownToolBypassesApprovalWithoutStaticBuildClaims(t *testing.T) {
	session := &fakeRuneSession{onCall: func(name string, _ json.RawMessage) (RuneCallResult, error) {
		if name != "brand_new_tool" {
			t.Fatalf("unexpected call %q", name)
		}
		return RuneCallResult{Content: `{"ok":true}`}, nil
	}}
	writes := &RuneWriteState{}
	specs := []RuneToolDef{
		{Name: "list_records", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "brand_new_tool", Description: "does something new", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	proposal, err := PrepareCMSApproval(context.Background(), session, "rune", "cms__brand_new_tool", json.RawMessage(`{}`))
	if err != nil || proposal.Required {
		t.Fatalf("an unclassified tool must not require approval: %+v err=%v", proposal, err)
	}
	tools := BuildRuneTools(specs, session, nil, writes)
	status, result := executeRune(t, tools, "cms__brand_new_tool", `{}`)
	if status != "completed" || !strings.HasPrefix(result.Content, `{"ok":true}`) {
		t.Fatalf("status=%q content=%q", status, result.Content)
	}
	if strings.Contains(result.Content, "static build") || result.Summary != "" {
		t.Fatalf("unknown tool must not claim static-build semantics: %q / %q", result.Content, result.Summary)
	}
	// The membership guard still runs.
	guarded := BuildRuneTools(specs, session, func(context.Context) error { return errors.New("stale") }, writes)
	status, blocked := executeRune(t, guarded, "cms__brand_new_tool", `{}`)
	if status != "failed" || !strings.Contains(blocked.Content, "no longer available") {
		t.Fatalf("unknown tool must honour the guard: %q", blocked.Content)
	}
}

// A transport failure on an unknown tool marks the turn uncertain: it may have
// written, so later writes stop.
func TestRuneUnknownToolTransportFailureMarksUncertain(t *testing.T) {
	session := &fakeRuneSession{onCall: func(string, json.RawMessage) (RuneCallResult, error) {
		return RuneCallResult{}, errors.New("timeout after dispatch")
	}}
	writes := &RuneWriteState{}
	tools := BuildRuneTools([]RuneToolDef{
		{Name: "brand_new_tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "create_record", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, session, nil, writes)
	if status, _ := executeRune(t, tools, "cms__brand_new_tool", `{}`); status != "failed" {
		t.Fatalf("status=%q, want failed", status)
	}
	if !writes.Uncertain() {
		t.Fatal("an unknown tool may write: its transport failure must mark the turn uncertain")
	}
	if status, res := executeRune(t, tools, "cms__create_record", `{}`); status != "failed" || !strings.Contains(res.Content, "unknown outcome") {
		t.Fatalf("known write must be blocked afterwards: %q", res.Content)
	}
}

// Only discovered tools are built, so a guessed name is never registered.
func TestBuildRuneToolsRegistersOnlyDiscoveredNames(t *testing.T) {
	session := &fakeRuneSession{}
	tools := BuildRuneTools([]RuneToolDef{
		{Name: "list_records", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "brand_new_tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "bad name", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, session, nil, &RuneWriteState{})
	registry := NewRegistry()
	for _, tool := range tools {
		if err := registry.Add(tool); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := registry.Get("cms__delete_content"); ok {
		t.Error("a name absent from the session was registered")
	}
	if _, ok := registry.Get("cms__bad name"); ok {
		t.Error("an invalid dynamic name was registered")
	}
}
