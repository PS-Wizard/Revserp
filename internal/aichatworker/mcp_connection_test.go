package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// fakeMCPDecryptor reuses the existing GSC secret path shape without Google.
type fakeMCPDecryptor struct {
	token string
	err   error
}

func (f *fakeMCPDecryptor) DecryptSecret(encrypted string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.token != "" {
		return f.token, nil
	}
	return "token-for:" + encrypted, nil
}

func (f *fakeMCPDecryptor) EncryptSecret(plain string) (string, error) { return "enc:" + plain, nil }

func (f *fakeMCPDecryptor) RefreshAccessToken(context.Context, string) (gsc.TokenResponse, error) {
	return gsc.TokenResponse{}, errors.New("no google in tests")
}

func (f *fakeMCPDecryptor) FetchQueriesCached(context.Context, string, string, string, gsc.QueryPageOptions) (gsc.QueryPage, error) {
	return gsc.QueryPage{}, errors.New("no google in tests")
}

func (f *fakeMCPDecryptor) FetchOverviewCached(context.Context, string, string, string) (gsc.OverviewPayload, error) {
	return gsc.OverviewPayload{}, errors.New("no google in tests")
}

func (f *fakeMCPDecryptor) FetchSummaryCached(context.Context, string, string, string, int) (gsc.SummaryPayload, error) {
	return gsc.SummaryPayload{}, errors.New("no google in tests")
}

// fakeMCPSession is a local fake connector: no network, injectable in tests
// only. Production dials through the wired connector.
type fakeMCPSession struct {
	mu       sync.Mutex
	tools    []aichattools.MCPToolDef
	calls    int
	endpoint string
	token    string
	closed   bool
	onCall   func(name string, args json.RawMessage) (aichattools.MCPResult, error)
}

func (f *fakeMCPSession) Tools() []aichattools.MCPToolDef { return f.tools }

func (f *fakeMCPSession) Call(ctx context.Context, name string, args json.RawMessage) (aichattools.MCPResult, error) {
	f.mu.Lock()
	f.calls++
	onCall := f.onCall
	f.mu.Unlock()
	if onCall != nil {
		return onCall(name, args)
	}
	return aichattools.MCPResult{Content: `{"ok":true}`}, nil
}

func (f *fakeMCPSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func liveMCPTools() []aichattools.MCPToolDef {
	return []aichattools.MCPToolDef{
		{Name: "fetch_items", Description: "remote", InputSchema: json.RawMessage(`{"type":"object","properties":{"filter":{"type":"string"}},"live":true}`)},
		{Name: "save_item", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func liveMCPSession() *fakeMCPSession {
	return &fakeMCPSession{tools: liveMCPTools()}
}

// insertMCPConnection saves one named connection for the test project with
// the given stored discovered set, like /connect and /check persist it, and
// returns its id, which the alias derivation needs.
func insertMCPConnection(t *testing.T, w *Worker, project pgtype.UUID, name, service, revision string, tools []aichattools.MCPToolDef) pgtype.UUID {
	t.Helper()
	stored := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		stored = append(stored, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema})
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var id pgtype.UUID
	if err := w.pool.QueryRow(context.Background(), `INSERT INTO project_mcp_connections(project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at) VALUES($1, $2, $3, $4, $5, $6::uuid, $7, now()) RETURNING id`,
		project, name, service, "https://mcp.example.test/mcp", "enc:secret-token", revision, raw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(context.Background(), `DELETE FROM project_mcp_connections WHERE id = $1`, id)
	})
	return id
}

func TestFilterMCPSpecs(t *testing.T) {
	connID := "11111111-1111-1111-1111-111111111111"
	specs := []aichattools.MCPToolDef{{Name: "fetch_items"}, {Name: "save_item"}}
	alias := aichattools.MCPModelToolName(connID, "save_item")
	filtered := filterMCPSpecs(specs, connID, []string{alias}, nil)
	if len(filtered) != 1 || filtered[0].Name != "fetch_items" {
		t.Fatalf("filtered = %v, want only fetch_items", filtered)
	}
	denied := filterMCPSpecs(specs, connID, nil, map[string]bool{"fetch_items": true})
	if len(denied) != 1 || denied[0].Name != "save_item" {
		t.Fatalf("denied = %v, want only save_item", denied)
	}
	if got := filterMCPSpecs(specs, connID, []string{"read_issues"}, nil); len(got) != 2 {
		t.Fatalf("unrelated denylist filtered specs: %v", got)
	}
}

func TestMCPModelToolNameSeparatesServers(t *testing.T) {
	first := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "save_item")
	second := aichattools.MCPModelToolName("22222222-2222-2222-2222-222222222222", "save_item")
	if first == second {
		t.Fatal("two servers with identical tool names share an alias")
	}
	renamed := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "save_item_v2")
	if renamed == first {
		t.Fatal("a renamed tool keeps its alias instead of starting at Ask")
	}
	if !aichattools.IsMCPModelToolName(first) || aichattools.IsMCPModelToolName("save_item") {
		t.Fatal("alias shape check wrong")
	}
}

func TestMCPSchemaDigestStable(t *testing.T) {
	pretty, err := mcpSchemaDigest(json.RawMessage("{\n  \"type\": \"object\",\n  \"properties\": {\"b\": {\"type\": \"string\"}, \"a\": {\"type\": \"string\"}}\n}"))
	if err != nil {
		t.Fatal(err)
	}
	compact, err := mcpSchemaDigest(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if pretty != compact {
		t.Fatal("equivalent schemas digest differently")
	}
	changed, err := mcpSchemaDigest(json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if changed == compact {
		t.Fatal("changed schema digests equal")
	}
}

func TestPreflightSessionEnforcesAllow(t *testing.T) {
	inner := &fakeMCPSession{tools: []aichattools.MCPToolDef{{Name: "fetch_items"}}}
	allowed := &preflightMCPSession{inner: inner, check: func(context.Context, string) error { return nil }}
	if _, err := allowed.Call(context.Background(), "fetch_items", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("allowed preflight blocked: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("allowed preflight calls = %d, want 1", inner.calls)
	}
	blocked := &preflightMCPSession{inner: inner, check: func(context.Context, string) error { return errors.New("allow prerequisite read tool") }}
	_, err := blocked.Call(context.Background(), "fetch_items", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "allow prerequisite read tool") {
		t.Fatalf("unallowed preflight err = %v, want the prerequisite message", err)
	}
	if !errors.Is(err, aichattools.ErrMCPPreflightPermission) {
		t.Fatalf("unallowed preflight err = %v, want the permission sentinel wrapped", err)
	}
	if inner.calls != 1 {
		t.Fatal("blocked preflight reached the session")
	}
}

func TestAllowedToolsFromSameRegistry(t *testing.T) {
	registry := aichattools.NewFilteredRegistry([]string{"read_issues"})
	if _, ok := registry.Get("read_issues"); ok {
		t.Fatal("executing registry serves a disabled tool: the model could run it by guessing its name")
	}
	allowed := allowedToolsFromRegistry(registry)
	for _, def := range allowed {
		if def.Name == "read_issues" {
			t.Fatal("provider defs offer a disabled tool")
		}
	}
	if len(allowed) != len(registry.Defs()) {
		t.Fatalf("allowed defs = %d, registry defs = %d: both must derive from the same registry", len(allowed), len(registry.Defs()))
	}
	alias := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "save_item")
	if err := registry.Add(aichattools.Tool{Def: aichattools.Def{Name: alias, Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, def := range allowedToolsFromRegistry(registry) {
		if def.Name == alias {
			found = true
		}
	}
	if !found {
		t.Fatal("per-turn MCP tool missing from the same-registry defs")
	}
}

func TestNormalizeToolResultIsError(t *testing.T) {
	alias := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "fetch_items")
	status, result := normalizeToolCallResult(alias, "completed", aichattools.Result{Content: alias + " error: no such collection"})
	if status != "failed" {
		t.Fatalf("status = %q, want failed for an isError tool failure", status)
	}
	if result.Summary != "no such collection" {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestToolArgsForLog(t *testing.T) {
	alias := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "save_item")
	if got := toolArgsForLog(alias, `{"title":"secret"}`); got != "[redacted]" {
		t.Fatalf("mcp args logged as %q, want redaction", got)
	}
	if got := toolArgsForLog("cms__update_record", `{"data":"x"}`); got != "[redacted]" {
		t.Fatalf("historical cms args logged as %q, want redaction", got)
	}
	if got := toolArgsForLog("read_issues", `{"limit":5}`); got != `{"limit":5}` {
		t.Fatalf("native args = %q", got)
	}
}

func TestMCPNoConnectionKeepsNativeChat(t *testing.T) {
	a, _, user, project := testWorker(t)
	a.GSC = &fakeMCPDecryptor{}
	a.MCPDial = func(context.Context, string, string) (aichattools.MCPSession, error) {
		t.Error("connector must not be dialed without a saved connection")
		return nil, errors.New("must not dial")
	}
	provider := &roundProvider{rounds: [][]ai.Event{{{Text: "native answer"}}}}
	a.provider = provider
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	if len(provider.requests) != 1 {
		t.Fatalf("streams = %d, want 1", len(provider.requests))
	}
	for _, def := range provider.requests[0].Tools {
		if strings.HasPrefix(def.Name, "mcp_") {
			t.Fatalf("provider offered %q without a connection", def.Name)
		}
	}
	system := provider.requests[0].Messages[0].Content
	if !strings.Contains(system, "not connected") {
		t.Fatalf("system status missing a brief not-connected note: %q", system[len(system)-500:])
	}
	for _, secret := range []string{"secret-token", "mcp.example"} {
		if strings.Contains(system, secret) {
			t.Fatalf("system leaks connection secret %q", secret)
		}
	}
	var status, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "native answer" {
		t.Fatalf("status=%q content=%q, want native chat to work", status, content)
	}
}

func TestMCPConnectionIsPerProject(t *testing.T) {
	a, _, user, project := testWorker(t)
	const revision = "11111111-1111-1111-1111-111111111111"
	insertMCPConnection(t, a, project, "Main", "custom", revision, liveMCPTools())

	if conns := a.loadMCPConnections(context.Background(), user, project); len(conns) != 1 {
		t.Fatalf("connections = %d, want the owning project's connection", len(conns))
	}
	var otherProject pgtype.UUID
	if err := a.pool.QueryRow(context.Background(), `INSERT INTO projects(organization_id,name,base_url) SELECT organization_id,'other','https://other.test' FROM projects WHERE id=$1 RETURNING id`, project).Scan(&otherProject); err != nil {
		t.Fatal(err)
	}
	if conns := a.loadMCPConnections(context.Background(), user, otherProject); len(conns) != 0 {
		t.Fatal("project B loaded project A's connection")
	}
	var outsider pgtype.UUID
	if err := a.pool.QueryRow(context.Background(), `INSERT INTO users(auth_provider,auth_subject,email) VALUES('test','mcp-outsider','mcp-outsider@x.test') RETURNING id`).Scan(&outsider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, outsider) })
	if conns := a.loadMCPConnections(context.Background(), outsider, project); len(conns) != 0 {
		t.Fatal("a non-member loaded the project connection")
	}
}

func TestMCPGuardRejectsReplacementAndDisconnect(t *testing.T) {
	a, _, user, project := testWorker(t)
	const first = "11111111-1111-1111-1111-111111111111"
	const second = "22222222-2222-2222-2222-222222222222"
	connID := insertMCPConnection(t, a, project, "Main", "custom", first, liveMCPTools())
	scope := turnScope{UserID: user, ProjectID: project}
	if _, err := a.checkMCPCall(context.Background(), scope, connID, first, "save_item"); err != nil {
		t.Fatalf("fresh check = %v, want nil", err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE project_mcp_connections SET revision = $2 WHERE id = $1`, connID, second); err != nil {
		t.Fatal(err)
	}
	if _, err := a.checkMCPCall(context.Background(), scope, connID, first, "save_item"); err == nil {
		t.Fatal("replaced revision still accepted: stale calls must be rejected")
	}
	if _, err := a.pool.Exec(context.Background(), `DELETE FROM project_mcp_connections WHERE project_id = $1`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := a.checkMCPCall(context.Background(), scope, connID, second, "save_item"); err == nil {
		t.Fatal("disconnected connection still accepted")
	}
}

func TestMCPDenyAbsentFromDefsNeverDispatches(t *testing.T) {
	a, _, user, project := testWorker(t)
	connID := insertMCPConnection(t, a, project, "Main", "custom", "44444444-4444-4444-4444-444444444444", liveMCPTools())
	a.GSC = &fakeMCPDecryptor{token: "live-token"}
	session := liveMCPSession()
	a.MCPDial = func(context.Context, string, string) (aichattools.MCPSession, error) { return session, nil }
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	provider := &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "deny-1", Name: alias, Args: `{"title":"x"}`}}},
		{{Text: "answered without the write"}},
	}}
	a.provider = provider
	id := queued(t, a, user, project)
	// The Deny lands after the turn exists but before the call dispatches.
	if _, err := a.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'save_item', 'deny')`, connID); err != nil {
		t.Fatal(err)
	}
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	for _, def := range provider.requests[0].Tools {
		if def.Name == alias {
			t.Fatalf("denied tool offered to the provider: %v", provider.requests[0].Tools)
		}
	}
	system := provider.requests[0].Messages[0].Content
	if !strings.Contains(system, "mcp_*") || strings.Contains(system, "mcp__*") {
		t.Fatalf("system prompt names the tools untruthfully: %.200q", system)
	}
	var callStatus, result string
	if err := a.pool.QueryRow(context.Background(), `SELECT status, result_content FROM ai_tool_calls WHERE turn_id = $1`, id).Scan(&callStatus, &result); err != nil {
		t.Fatal(err)
	}
	if callStatus != "failed" || !strings.Contains(result, "unknown tool") {
		t.Fatalf("denied call = %q/%q, want failed unknown tool: denied tools stay absent from the defs", callStatus, result)
	}
	var approvals int
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1`, id).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatal("denied call created an approval row")
	}
	if session.calls != 0 {
		t.Fatal("denied tool reached the MCP session")
	}
}

func TestMCPDisabledToolCannotRunByGuessing(t *testing.T) {
	a, _, user, project := testWorker(t)
	connID := insertMCPConnection(t, a, project, "Main", "custom", "55555555-5555-5555-5555-555555555555", liveMCPTools())
	a.GSC = &fakeMCPDecryptor{token: "live-token"}
	session := liveMCPSession()
	a.MCPDial = func(context.Context, string, string) (aichattools.MCPSession, error) { return session, nil }
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	provider := &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "guess-1", Name: alias, Args: `{}`}}},
		{{Text: "answered without the write"}},
	}}
	a.provider = provider
	id := queued(t, a, user, project)
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET disabled_ai_tools = $2 WHERE id = $1`, id, []string{alias}); err != nil {
		t.Fatal(err)
	}
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	for _, def := range provider.requests[0].Tools {
		if def.Name == alias {
			t.Fatalf("disabled MCP tool offered to the provider: %v", provider.requests[0].Tools)
		}
	}
	var callStatus, result string
	if err := a.pool.QueryRow(context.Background(), `SELECT status, result_content FROM ai_tool_calls WHERE turn_id = $1`, id).Scan(&callStatus, &result); err != nil {
		t.Fatal(err)
	}
	if callStatus != "failed" || !strings.Contains(result, "unknown tool") {
		t.Fatalf("guessed disabled call = %q/%q, want failed unknown tool", callStatus, result)
	}
	if session.calls != 0 {
		t.Fatal("disabled tool reached the MCP session")
	}
}

func TestRecoveredMCPWriteNeverRequeues(t *testing.T) {
	a, _, user, project := testWorker(t)
	id := queued(t, a, user, project)
	alias := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "save_item")
	if _, err := a.pool.Exec(context.Background(), `INSERT INTO ai_tool_calls(turn_id, seq, call_id, name, args, status) VALUES($1, 0, 'mcp-crash', $2, '{}', 'running')`, id, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET status='running', claimed_by='dead', attempt_count=1, lease_expires_at=now()-interval '1 second', output_started_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := a.pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("recovered write turn = %q, want failed (a recovered write must never requeue and run twice)", status)
	}
}

func TestRequeueRefusesAfterOutputStarted(t *testing.T) {
	a, _, user, project := testWorker(t)
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET output_started_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.requeue(claimed); err == nil {
		t.Fatal("requeue after output_started_at = nil, want a refusal")
	}
	var status string
	if err := a.pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("status = %q, want running (no state change on refusal)", status)
	}
}

// TestVerifyCurrentBlocksRemovedTool is the mid-turn rediscovery rule: the
// tool leaves the current stored set while the revision is unchanged, so
// verification fails and no stale execution can start.
func TestVerifyCurrentBlocksRemovedTool(t *testing.T) {
	a, _, user, project := testWorker(t)
	connID := insertMCPConnection(t, a, project, "Main", "custom", "11111111-1111-1111-1111-111111111111", liveMCPTools())
	scope := turnScope{UserID: user, ProjectID: project}
	if _, _, err := a.verifyMCPCallCurrent(context.Background(), scope, connID, "11111111-1111-1111-1111-111111111111", "save_item"); err != nil {
		t.Fatalf("fresh verify = %v, want nil", err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE project_mcp_connections SET tools = '[]' WHERE id = $1`, connID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.verifyMCPCallCurrent(context.Background(), scope, connID, "11111111-1111-1111-1111-111111111111", "save_item"); err == nil {
		t.Fatal("removed tool still verifies: stale execution not blocked")
	}
}

// TestMCPGuardExecutionBoundary pins the execution-start rule: a direct
// call needs Allow at dispatch, an approved Ask call runs under its valid
// decision, and Deny blocks both.
func TestMCPGuardExecutionBoundary(t *testing.T) {
	a, _, user, project := testWorker(t)
	connID := insertMCPConnection(t, a, project, "Main", "custom", "22222222-2222-2222-2222-222222222222", liveMCPTools())
	scope := turnScope{UserID: user, ProjectID: project}
	handle := &mcpTurnHandle{connectionID: connID, revision: "22222222-2222-2222-2222-222222222222", session: liveMCPSession()}
	set := &mcpHandleSet{byAlias: map[string]*mcpTurnHandle{}, byName: map[string]string{}}
	setPermission := func(permission string) {
		t.Helper()
		if _, err := a.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'save_item', $2) ON CONFLICT (connection_id, tool_name) DO UPDATE SET permission = $2`, connID, permission); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	setPermission("allow")
	if err := a.mcpCallGuard(scope, set, handle, "save_item", "alias-1")(ctx); err != nil {
		t.Fatalf("direct allow guard = %v, want nil", err)
	}
	// The rule flips to Ask after the gate: the direct call must block
	// instead of executing without an approval.
	setPermission("ask")
	if err := a.mcpCallGuard(scope, set, handle, "save_item", "alias-1")(ctx); err == nil {
		t.Fatal("direct call with Ask rule passed the execution-start guard")
	}
	// The same Ask call with a valid exact decision just marked executing
	// still runs.
	set.markApproved("alias-1")
	if err := a.mcpCallGuard(scope, set, handle, "save_item", "alias-1")(ctx); err != nil {
		t.Fatalf("approved ask guard = %v, want nil", err)
	}
	// Deny blocks everything, decided or not.
	setPermission("deny")
	if err := a.mcpCallGuard(scope, set, handle, "save_item", "alias-1")(ctx); err == nil {
		t.Fatal("denied approved call passed the execution-start guard")
	}
}
