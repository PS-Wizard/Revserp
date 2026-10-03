package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testBearer = "test-bearer-token"

func objSchema(props string) json.RawMessage {
	if props == "" {
		return json.RawMessage(`{"type":"object","additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":false}`)
}

var baseSchemas = map[string]json.RawMessage{
	"list_collections":      objSchema(""),
	"get_collection_schema": objSchema(`"name":{"type":"string"}`),
	"list_records":          objSchema(`"collection":{"type":"string"}`),
	"read_record":           objSchema(`"collection":{"type":"string"},"id":{"type":"string"}`),
	"create_record":         objSchema(`"collection":{"type":"string"},"data":{"type":"object"}`),
	"update_record":         objSchema(`"collection":{"type":"string"},"id":{"type":"string"},"data":{"type":"object"}`),
}

type toolDef struct {
	name    string
	schema  json.RawMessage
	handler mcp.ToolHandler
}

func echoOK(body string) mcp.ToolHandler {
	return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: body}}}, nil
	}
}

// optionally enforcing a bearer token. calls counts tool executions so a test
// can prove discovery never runs a tool.
func newTestServer(t *testing.T, bearer string, defs []toolDef, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-test", Version: "0.0.1"}, nil)
	for _, d := range defs {
		d := d
		srv.AddTool(&mcp.Tool{
			Name:        d.name,
			Description: "test " + d.name,
			InputSchema: d.schema,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if calls != nil {
				calls.Add(1)
			}
			return d.handler(ctx, req)
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var wrapped http.Handler = h
	if bearer != "" {
		wrapped = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+bearer {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return ts
}

func baseDefs(handler mcp.ToolHandler) []toolDef {
	names := []string{"list_collections", "get_collection_schema", "list_records", "read_record", "create_record", "update_record"}
	defs := make([]toolDef, 0, len(names))
	for _, n := range names {
		defs = append(defs, toolDef{name: n, schema: baseSchemas[n], handler: handler})
	}
	return defs
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustConnect(t *testing.T, ctx context.Context, endpoint, bearer string) *Session {
	t.Helper()
	s, err := connectWithHTTPClient(ctx, endpoint, bearer, nil)
	if err != nil {
		t.Fatalf("connect: %v (code=%s)", err, ErrorCode(err))
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// JSON, so a malformed protocol is rejected at connect time.
func newMCPGarbageServer(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<html>not mcp</html>"))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestDiscoveryAppliesNoNameHeuristic pins the deliberate behaviour change: this
// package no longer filters tools by name. Every historically "restricted" name
// is discovered and returned. The saved Ask/Allow/Deny permission, not a name,
// decides what may run, so a marketplace cannot be gated by a five-word list
// that delete_record and drop_table walk straight past.
func TestDiscoveryAppliesNoNameHeuristic(t *testing.T) {
	formerlyRestricted := []string{
		"read_file", "write_to_disk", "run_sql", "exec_command", "shell_exec",
		"batch_update_content", "describe_tables", "create_child_theme",
		"delete_record", "drop_table", "destroy_site", "truncate_content",
	}
	defs := baseDefs(echoOK("ok"))
	for _, n := range formerlyRestricted {
		defs = append(defs, toolDef{name: n, schema: objSchema(""), handler: echoOK("ok")})
	}
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)

	seen := map[string]bool{}
	for _, tl := range s.Tools() {
		seen[tl.Name] = true
	}
	for _, n := range formerlyRestricted {
		if !seen[n] {
			t.Errorf("tool %q was filtered by name; discovery must not judge tool names", n)
		}
	}
}

// TestIsValidToolNameMatchesProtocolGrammar pins the go-sdk grammar rather than a
// legacy provider-namespace rule. Dots and doubled underscores are legal MCP
// names, and a leading separator is legal too; rejecting them would silently
// hide tools from servers that use them.
func TestIsValidToolNameMatchesProtocolGrammar(t *testing.T) {
	good := []string{
		"read_record", "a", "A1", "list-records", "get_item_v2",
		"service__read", "tools.read", "_leading", "-leading", ".dotted",
		strings.Repeat("x", maxToolName),
	}
	for _, n := range good {
		if !IsValidToolName(n) {
			t.Errorf("valid name %q rejected", n)
		}
	}
	bad := []string{
		"", "has space", "has/slash", "has:colon", "üñî", "tab\there", "new\nline",
		strings.Repeat("x", maxToolName+1),
	}
	for _, n := range bad {
		if IsValidToolName(n) {
			t.Errorf("invalid name %q accepted", n)
		}
	}
}

func TestConnectDiscoversEveryValidTool(t *testing.T) {
	var calls atomic.Int64
	defs := baseDefs(echoOK("ok"))
	defs = append(defs, toolDef{name: "brand_new_tool", schema: objSchema(""), handler: echoOK("pwned")})
	ts := newTestServer(t, testBearer, defs, &calls)

	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	seen := map[string]bool{}
	for _, tl := range s.Tools() {
		seen[tl.Name] = true
		if len(tl.InputSchema) == 0 {
			t.Errorf("tool %s has empty schema", tl.Name)
		}
		var sch map[string]any
		if err := json.Unmarshal(tl.InputSchema, &sch); err != nil || sch["type"] != "object" {
			t.Errorf("tool %s schema is not a usable object", tl.Name)
		}
	}
	for name := range baseSchemas {
		if !seen[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	if !seen["brand_new_tool"] {
		t.Error("a newly advertised tool must be discovered")
	}
	if res, err := s.Call(testCtx(t), "brand_new_tool", json.RawMessage(`{}`)); err != nil || res.IsError || res.Content != "pwned" {
		t.Errorf("new tool call: res=%+v err=%v", res, err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Connect executed %d tools; discovery must never execute tools", n)
	}
}

func TestConnectAuthFailure(t *testing.T) {
	ts := newTestServer(t, testBearer, baseDefs(echoOK("ok")), nil)
	_, err := connectWithHTTPClient(testCtx(t), ts.URL, "wrong-token", nil)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if got := ErrorCode(err); got != string(CodeUnauthorized) {
		t.Fatalf("code = %q, want unauthorized", got)
	}
	if strings.Contains(err.Error(), "wrong-token") || strings.Contains(err.Error(), ts.URL) {
		t.Fatal("error leaks token or URL")
	}
}

func TestCallPrefersStructuredContentOnce(t *testing.T) {
	structured := map[string]any{"collections": []any{map[string]any{"name": "posts", "type": "base"}}}
	dup, _ := json.Marshal(structured)
	defs := baseDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.Name == "list_collections" {
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(dup)}},
				StructuredContent: structured,
			}, nil
		}
		return echoOK("ok")(ctx, req)
	})
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)

	res, err := s.Call(testCtx(t), "list_collections", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatal("unexpected isError")
	}
	if res.Content != string(dup) {
		t.Fatalf("structured content not preferred verbatim: %q", res.Content)
	}
}

func TestCallTextFallbackAndIsError(t *testing.T) {
	defs := baseDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		switch req.Params.Name {
		case "read_record":
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "hello "},
				&mcp.TextContent{Text: "world"},
			}}, nil
		case "list_records":
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "no such collection"}},
			}, nil
		}
		return echoOK("ok")(ctx, req)
	})
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	ctx := testCtx(t)

	res, err := s.Call(ctx, "read_record", json.RawMessage(`{"collection":"c","id":"1"}`))
	if err != nil || res.IsError || res.Content != "hello world" {
		t.Fatalf("text fallback: res=%+v err=%v", res, err)
	}
	eres, err := s.Call(ctx, "list_records", json.RawMessage(`{"collection":"nope"}`))
	if err != nil {
		t.Fatalf("isError must not be a Go error: %v", err)
	}
	if !eres.IsError || eres.Content != "no such collection" {
		t.Fatalf("isError result: %+v", eres)
	}
}

func TestCallUnknownToolAndBadArgs(t *testing.T) {
	ts := newTestServer(t, testBearer, baseDefs(echoOK("ok")), nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	ctx := testCtx(t)

	if _, err := s.Call(ctx, "evil_tool", json.RawMessage(`{}`)); ErrorCode(err) != string(CodeInvalidTools) {
		t.Fatalf("unknown tool code = %q", ErrorCode(err))
	}
	if _, err := s.Call(ctx, "read_record", json.RawMessage(`[1,2]`)); ErrorCode(err) != string(CodeInvalidTools) {
		t.Fatalf("array args code = %q", ErrorCode(err))
	}
	big := `{"k":"` + strings.Repeat("x", maxArgsBytes) + `"}`
	if _, err := s.Call(ctx, "read_record", json.RawMessage(big)); ErrorCode(err) != string(CodeTooLarge) {
		t.Fatalf("oversized args code = %q", ErrorCode(err))
	}
}

// TestOversizedArgsCheckedBeforeTrim pins that the byte cap runs on the raw
// argument bytes. Trimming first would let an oversized whitespace or null
// payload slip past a bound the caller is told is enforced.
func TestOversizedArgsCheckedBeforeTrim(t *testing.T) {
	ts := newTestServer(t, testBearer, baseDefs(echoOK("ok")), nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	ctx := testCtx(t)

	for _, args := range []json.RawMessage{
		json.RawMessage(strings.Repeat(" ", maxArgsBytes+1)),
		json.RawMessage(strings.Repeat("\n", maxArgsBytes+1)),
		json.RawMessage(strings.Repeat("null", maxArgsBytes)),
	} {
		if _, err := s.Call(ctx, "read_record", args); ErrorCode(err) != string(CodeTooLarge) {
			t.Errorf("len %d: code = %q, want too_large", len(args), ErrorCode(err))
		}
	}
}

// TestCallDoesNotRetryFailedWrite counts HTTP requests server-side. A mutating
// call that fails must reach the remote server exactly once; an automatic retry
// would duplicate a write whose outcome the client cannot know.
func TestCallDoesNotRetryFailedWrite(t *testing.T) {
	var toolCalls atomic.Int64
	defs := []toolDef{{
		name:   "update_record",
		schema: baseSchemas["update_record"],
		handler: func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			toolCalls.Add(1)
			return nil, errors.New("remote write failed after dispatch")
		},
	}}
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)

	if _, err := s.Call(testCtx(t), "update_record", json.RawMessage(`{"id":"1"}`)); err == nil {
		t.Fatal("expected the failing write to surface an error")
	}
	if n := toolCalls.Load(); n != 1 {
		t.Fatalf("tool executed %d times; a failed write must never be retried", n)
	}
}

func TestCallRejectsUnsupportedContent(t *testing.T) {
	defs := baseDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png"},
		}}, nil
	})
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	_, err := s.Call(testCtx(t), "read_record", json.RawMessage(`{"collection":"c","id":"1"}`))
	if err == nil {
		t.Fatal("expected unsupported content error")
	}
	if got := ErrorCode(err); got != string(CodeUnsupportedTransport) {
		t.Fatalf("code = %q, want unsupported_transport", got)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("message must say unsupported explicitly: %v", err)
	}
}

func TestCallTooLargeStructuredResult(t *testing.T) {
	defs := baseDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			StructuredContent: map[string]any{"blob": strings.Repeat("y", maxResultBytes)},
		}, nil
	})
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	_, err := s.Call(testCtx(t), "list_collections", json.RawMessage(`{}`))
	if got := ErrorCode(err); got != string(CodeTooLarge) {
		t.Fatalf("code = %q, want too_large", got)
	}
}

func TestOversizedSchemaExcluded(t *testing.T) {
	huge := `{"type":"object","properties":{"big":{"type":"string","description":"` +
		strings.Repeat("z", maxSchemaBytes) + `"}}}`

	var defs []toolDef
	for name, sch := range baseSchemas {
		if name == "list_records" {
			sch = json.RawMessage(huge)
		}
		defs = append(defs, toolDef{name: name, schema: sch, handler: echoOK("ok")})
	}
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	for _, tl := range s.Tools() {
		if tl.Name == "list_records" {
			t.Fatal("oversized-schema tool must be excluded")
		}
	}
	if len(s.Tools()) != 5 {
		t.Fatalf("got %d tools, want 5 after exclusion", len(s.Tools()))
	}
}

func TestRedirectRefusedWithoutLeak(t *testing.T) {
	var secondHits atomic.Int64
	var leakedAuth atomic.Int64
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			leakedAuth.Add(1)
		}
	}))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/mcp", http.StatusFound)
	}))
	t.Cleanup(first.Close)

	_, err := connectWithHTTPClient(testCtx(t), first.URL+"/mcp", testBearer, nil)
	if err == nil {
		t.Fatal("expected redirect refusal")
	}
	if got := ErrorCode(err); got != string(CodeUnsupportedTransport) && got != string(CodeUnreachable) {
		t.Fatalf("code = %q, want unsupported_transport", got)
	}
	if n := secondHits.Load(); n != 0 {
		t.Fatalf("redirect target hit %d times; token may have leaked", n)
	}
	if n := leakedAuth.Load(); n != 0 {
		t.Fatal("authorization header leaked to redirect target")
	}
}

func TestCallTimeout(t *testing.T) {
	defs := baseDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return echoOK("slow")(ctx, req)
		}
	})
	ts := newTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := s.Call(ctx, "list_collections", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected timeout")
	}
	if got := ErrorCode(err); got != string(CodeTimeout) {
		t.Fatalf("code = %q, want timeout", got)
	}
}

func TestErrorCodeContract(t *testing.T) {
	if got := ErrorCode(nil); got != "" {
		t.Fatalf("nil -> %q", got)
	}
	if got := ErrorCode(errors.New("boom")); got != "" {
		t.Fatalf("foreign -> %q", got)
	}
	if got := ErrorCode(context.DeadlineExceeded); got != string(CodeTimeout) {
		t.Fatalf("deadline -> %q", got)
	}
	wrapped := &Error{Code: CodeUnauthorized, msg: "x", inner: errors.New("y")}
	if got := ErrorCode(wrapped); got != string(CodeUnauthorized) {
		t.Fatalf("typed -> %q", got)
	}
	if got := ErrorCode(errors.Join(errors.New("a"), wrapped)); got != string(CodeUnauthorized) {
		t.Fatalf("joined -> %q", got)
	}
	_, err := connectWithHTTPClient(testCtx(t), "http://[::1]/", "tok\nx", nil)
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if msg := err.Error(); strings.Contains(msg, "tok") && strings.Contains(msg, "\n") {
		t.Fatalf("unsafe message: %q", msg)
	}
}

func TestCloseIdempotent(t *testing.T) {
	ts := newTestServer(t, testBearer, baseDefs(echoOK("ok")), nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := s.Call(testCtx(t), "list_collections", nil); ErrorCode(err) != string(CodeUnreachable) {
		t.Fatalf("call after close code = %q", ErrorCode(err))
	}
}

// leaking its pooled transport once the turn that owned it ends.
func TestSessionClosesIdleConnectionsOnce(t *testing.T) {
	ts := newTestServer(t, testBearer, baseDefs(echoOK("ok")), nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)

	var closes atomic.Int64
	s.closeHTTP = func() { closes.Add(1) }
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if n := closes.Load(); n != 1 {
		t.Fatalf("idle connections closed %d times, want exactly 1", n)
	}
}

func TestConnectRejectsMalformedProtocol(t *testing.T) {
	if _, err := connectWithHTTPClient(testCtx(t), newMCPGarbageServer(t), testBearer, nil); err == nil {
		t.Fatal("a non-MCP JSON body must be rejected at connect time")
	}
}

func TestConnectSucceedsWithNoTools(t *testing.T) {
	ts := newTestServer(t, testBearer, nil, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	if n := len(s.Tools()); n != 0 {
		t.Fatalf("got %d tools, want 0: a server with nothing enabled is a valid session", n)
	}
}

func TestOversizedJSONBodyOnConnect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"blob":"` + strings.Repeat("x", 2<<20) + `"}`))
	}))
	t.Cleanup(ts.Close)
	_, err := connectWithHTTPClient(testCtx(t), ts.URL, testBearer, nil)
	if err == nil {
		t.Fatal("expected oversized body error")
	}
	if got := ErrorCode(err); got != string(CodeTooLarge) {
		t.Fatalf("code = %q, want too_large", got)
	}
}

func TestOversizedJSONBodyOnCall(t *testing.T) {
	big := strings.Repeat("z", 2<<20)
	ts := newTestServer(t, testBearer, baseDefs(echoOK(big)), nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	_, err := s.Call(testCtx(t), "list_collections", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected oversized body error")
	}
	if got := ErrorCode(err); got != string(CodeTooLarge) {
		t.Fatalf("code = %q, want too_large", got)
	}
}

type fakeLister struct {
	fn func(cursor string) (*mcp.ListToolsResult, error)
}

func (f *fakeLister) ListTools(_ context.Context, p *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return f.fn(p.Cursor)
}

func validToolPage(names []string, next string) *mcp.ListToolsResult {
	res := &mcp.ListToolsResult{NextCursor: next}
	for _, n := range names {
		res.Tools = append(res.Tools, &mcp.Tool{
			Name:        n,
			Description: "test " + n,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}
	return res
}

func TestDiscoveryCountsAllAdvertised(t *testing.T) {
	evil := make([]string, 0, MaxDiscoveredTools+1)
	for i := 0; i < MaxDiscoveredTools+1; i++ {
		evil = append(evil, "evil_tool")
	}
	fl := &fakeLister{fn: func(cursor string) (*mcp.ListToolsResult, error) {
		return validToolPage(evil, ""), nil
	}}
	_, err := discoverTools(testCtx(t), fl)
	if got := ErrorCode(err); got != string(CodeInvalidTools) {
		t.Fatalf("code = %q, want invalid_tools for over-advertised tools", got)
	}
}

func TestDiscoveryRejectsDuplicateNames(t *testing.T) {
	fl := &fakeLister{fn: func(cursor string) (*mcp.ListToolsResult, error) {
		if cursor == "" {
			return validToolPage([]string{"list_collections"}, "n"), nil
		}
		return validToolPage([]string{"list_collections"}, ""), nil
	}}
	_, err := discoverTools(testCtx(t), fl)
	if got := ErrorCode(err); got != string(CodeInvalidTools) {
		t.Fatalf("code = %q, want invalid_tools for duplicate tools", got)
	}
}

func TestDiscoveryRejectsCursorLoop(t *testing.T) {
	fl := &fakeLister{fn: func(cursor string) (*mcp.ListToolsResult, error) {
		if cursor == "" {
			return validToolPage([]string{"list_collections"}, "a"), nil
		}
		return validToolPage(nil, "a"), nil
	}}
	_, err := discoverTools(testCtx(t), fl)
	if got := ErrorCode(err); got != string(CodeInvalidTools) {
		t.Fatalf("code = %q, want invalid_tools for cursor loop", got)
	}
}

func TestMixedTextAndUnsupportedHasNotice(t *testing.T) {
	res := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "partial "},
		&mcp.ImageContent{Data: []byte{1}, MIMEType: "image/png"},
	}}
	out, err := renderResult("read_record", res)
	if err != nil {
		t.Fatalf("mixed content must not be a Go error: %v", err)
	}
	if !strings.HasPrefix(out.Content, "partial ") {
		t.Fatalf("text lost: %q", out.Content)
	}
	if !strings.Contains(out.Content, "unsupported") {
		t.Fatalf("missing explicit unsupported notice: %q", out.Content)
	}
}

func TestStructuredWithUnsupportedIsExplicit(t *testing.T) {
	res := &mcp.CallToolResult{
		StructuredContent: map[string]any{"a": "b"},
		Content:           []mcp.Content{&mcp.ImageContent{Data: []byte{1}, MIMEType: "image/png"}},
	}
	_, err := renderResult("list_collections", res)
	if got := ErrorCode(err); got != string(CodeUnsupportedTransport) {
		t.Fatalf("code = %q, want unsupported_transport", got)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("message must say unsupported explicitly: %v", err)
	}
}

func TestInvalidSchemasRejected(t *testing.T) {
	badType := &mcp.Tool{
		Name:        "list_records",
		Description: "bad type",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":42}}}`),
	}
	if _, ok := acceptTool(badType); ok {
		t.Error("schema with non-string type was accepted")
	}
	badRequired := &mcp.Tool{
		Name:        "list_records",
		Description: "bad required",
		InputSchema: json.RawMessage(`{"type":"object","required":"nope"}`),
	}
	if _, ok := acceptTool(badRequired); ok {
		t.Error("schema with non-array required was accepted")
	}
	remoteRef := &mcp.Tool{
		Name:        "read_record",
		Description: "remote ref",
		InputSchema: json.RawMessage(`{"type":"object","$ref":"https://example.com/schema.json"}`),
	}
	if _, ok := acceptTool(remoteRef); ok {
		t.Error("schema with remote $ref was accepted; no remote fetches allowed")
	}
	good := &mcp.Tool{
		Name:        "read_record",
		Description: "good",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}`),
	}
	if _, ok := acceptTool(good); !ok {
		t.Error("valid object schema was rejected")
	}
}
