package runecms

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

var sixSchemas = map[string]json.RawMessage{
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

// newRuneTestServer builds a stateless JSON MCP server with the given tools,
// optionally enforcing a bearer token. calls counts tool executions.
func newRuneTestServer(t *testing.T, bearer string, defs []toolDef, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "rune-test", Version: "0.0.1"}, nil)
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

func sixDefs(handler mcp.ToolHandler) []toolDef {
	names := []string{"list_collections", "get_collection_schema", "list_records", "read_record", "create_record", "update_record"}
	defs := make([]toolDef, 0, len(names))
	for _, n := range names {
		defs = append(defs, toolDef{name: n, schema: sixSchemas[n], handler: handler})
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

// newMCPGarbageServer answers every request with 200 and a body that is not
// MCP JSON, so a malformed protocol is rejected at connect time.
func newMCPGarbageServer(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<html>not mcp</html>"))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestConnectDiscoversDynamicTools(t *testing.T) {
	var calls atomic.Int64
	ts := newRuneTestServer(t, testBearer, append(sixDefs(echoOK("ok")),
		toolDef{name: "brand_new_tool", schema: objSchema(""), handler: echoOK("pwned")},
		toolDef{name: "read_file", schema: objSchema(""), handler: echoOK("pwned")},
	), &calls)

	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	tools := s.Tools()
	seen := map[string]bool{}
	for _, tl := range tools {
		seen[tl.Name] = true
		if len(tl.InputSchema) == 0 {
			t.Errorf("tool %s has empty schema", tl.Name)
		}
		var sch map[string]any
		if err := json.Unmarshal(tl.InputSchema, &sch); err != nil || sch["type"] != "object" {
			t.Errorf("tool %s schema is not a usable object", tl.Name)
		}
	}
	for name := range sixSchemas {
		if !seen[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	// A tool the client has never heard of is discovered and executable.
	if !seen["brand_new_tool"] {
		t.Error("a newly advertised tool must be discovered")
	}
	if res, err := s.Call(testCtx(t), "brand_new_tool", json.RawMessage(`{}`)); err != nil || res.IsError || res.Content != "pwned" {
		t.Errorf("new tool call: res=%+v err=%v", res, err)
	}
	// Filesystem access stays excluded.
	if seen["read_file"] {
		t.Error("restricted tool was enabled")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Connect executed %d tools; discovery must never execute tools", n)
	}
}

func TestConnectAuthFailure(t *testing.T) {
	ts := newRuneTestServer(t, testBearer, sixDefs(echoOK("ok")), nil)
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
	defs := sixDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.Name == "list_collections" {
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(dup)}},
				StructuredContent: structured,
			}, nil
		}
		return echoOK("ok")(ctx, req)
	})
	ts := newRuneTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)

	res, err := s.Call(testCtx(t), "list_collections", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatal("unexpected isError")
	}
	var got any
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if res.Content != string(dup) {
		t.Fatalf("structured content not preferred verbatim: %q", res.Content)
	}
}

func TestCallTextFallbackAndIsError(t *testing.T) {
	defs := sixDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	ts := newRuneTestServer(t, testBearer, defs, nil)
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
	ts := newRuneTestServer(t, testBearer, sixDefs(echoOK("ok")), nil)
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

func TestCallRejectsUnsupportedContent(t *testing.T) {
	defs := sixDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png"},
		}}, nil
	})
	ts := newRuneTestServer(t, testBearer, defs, nil)
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
	defs := sixDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			StructuredContent: map[string]any{"blob": strings.Repeat("y", maxResultBytes)},
		}, nil
	})
	ts := newRuneTestServer(t, testBearer, defs, nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	_, err := s.Call(testCtx(t), "list_collections", json.RawMessage(`{}`))
	if got := ErrorCode(err); got != string(CodeTooLarge) {
		t.Fatalf("code = %q, want too_large", got)
	}
}

func TestOversizedSchemaExcluded(t *testing.T) {
	huge := `{"type":"object","properties":{"big":{"type":"string","description":"` +
		strings.Repeat("z", maxSchemaBytes) + `"}}}`

	// Replace list_records schema with an oversized one; keep the rest.
	var defs2 []toolDef
	for name, sch := range sixSchemas {
		if name == "list_records" {
			sch = json.RawMessage(huge)
		}
		defs2 = append(defs2, toolDef{name: name, schema: sch, handler: echoOK("ok")})
	}
	ts := newRuneTestServer(t, testBearer, defs2, nil)
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

func TestForbiddenDestinations(t *testing.T) {
	ctx := testCtx(t)
	bad := []string{
		"https://user:pass@example.com/mcp", // credentials
		"https://example.com/mcp#frag",      // fragment
		"https://example.com:8080/mcp",      // unexpected port
		"https://169.254.169.254/",          // cloud metadata
		"https://127.0.0.1/",                // loopback
		"https://10.0.0.1/",                 // private
		"https://192.168.1.1/",              // private
		"https://172.16.5.5/",               // private
		"https://224.0.0.1/",                // multicast
		"https://0.0.0.0/",                  // unspecified
		"https://203.0.113.1/",              // documentation
		"https://100.64.0.1/",               // CGNAT
		"https://198.18.0.1/",               // benchmark
		"https://[::1]/",                    // ipv6 loopback
		"https://[::ffff:127.0.0.1]/",       // mapped loopback
		"https://[fe80::1]/",                // link-local
		"https://[2001:db8::1]/",            // documentation v6
		"https://[ff02::1]/",                // multicast v6
		"ftp://example.com/mcp",             // wrong scheme
		"not a url at all %%",               // malformed
		"",                                  // empty
	}
	for _, ep := range bad {
		if _, err := Connect(ctx, ep, testBearer); ErrorCode(err) != string(CodeInvalidEndpoint) {
			t.Errorf("endpoint %q: code = %q, want invalid_endpoint", ep, ErrorCode(err))
		}
	}
	for _, tok := range []string{"", "has space", "line\nbreak", "tab\there", strings.Repeat("a", maxBearerLen+1)} {
		if _, err := Connect(ctx, "https://example.com/mcp", tok); ErrorCode(err) != string(CodeInvalidToken) {
			t.Errorf("token %q: code = %q, want invalid_token", tok, ErrorCode(err))
		}
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
	defs := sixDefs(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return echoOK("slow")(ctx, req)
		}
	})
	ts := newRuneTestServer(t, testBearer, defs, nil)
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
	// Safe messages carry no secrets.
	_, err := connectWithHTTPClient(testCtx(t), "http://[::1]/", "tok\nx", nil)
	msg := err.Error()
	if strings.Contains(msg, "tok") && strings.Contains(msg, "\n") {
		t.Fatalf("unsafe message: %q", msg)
	}
}

func TestCloseIdempotent(t *testing.T) {
	ts := newRuneTestServer(t, testBearer, sixDefs(echoOK("ok")), nil)
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
