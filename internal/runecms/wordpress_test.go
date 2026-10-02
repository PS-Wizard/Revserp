package runecms

// WordPress transport profile tests: dynamic discovery, the exposure
// exclusions that matter (batch, filesystem, raw SQL), discovery bounds, and
// the reused HTTP policy.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// restrictedWordPressTools must never be exposed, whatever a server
// advertises. They are exposure exclusions, not approval policy.
var restrictedWordPressTools = []string{
	"batch",
	"read_file", "write_file", "edit_file", "delete_file", "restore_file", "create_child_theme",
	"sql_query", "sql_execute", "describe_tables", "exec_php", "shell_exec",
}

func wordPressDef(handler mcp.ToolHandler, names ...string) []toolDef {
	defs := make([]toolDef, 0, len(names))
	for _, name := range names {
		defs = append(defs, toolDef{name: name, schema: objSchema(`"id":{"type":"integer"}`), handler: handler})
	}
	return defs
}

func TestWordPressExposureExclusions(t *testing.T) {
	for _, name := range restrictedWordPressTools {
		if !exposureExcluded(name) {
			t.Errorf("%q must stay excluded from every session", name)
		}
	}
	// Content tools whose names merely contain a restricted word survive.
	for _, name := range []string{"get_content", "update_content", "set_featured_image", "batch_update_content", "bulk_set_image_alt"} {
		if exposureExcluded(name) {
			t.Errorf("%q must stay available", name)
		}
	}
	if !IsValidToolName("get_content") || !IsValidToolName("brand_new_tool_2026") {
		t.Error("IsValidToolName rejects valid dynamic names")
	}
	for _, bad := range []string{"", "-leading", "_leading", "has space", "cms__read_record", "semi;colon", strings.Repeat("x", maxToolName+1)} {
		if IsValidToolName(bad) {
			t.Errorf("IsValidToolName accepted %q", bad)
		}
	}
}

func TestConnectWordPressKeepsDiscoveredToolsExceptRestricted(t *testing.T) {
	var calls atomic.Int64
	names := []string{"get_content", "update_content", "publish_content", "set_seo", "brand_new_group_tool"}
	defs := append(wordPressDef(echoOK("ok"), names...), wordPressDef(echoOK("pwned"), restrictedWordPressTools...)...)
	ts := newRuneTestServer(t, testBearer, defs, &calls)

	s, err := connectWordPressWithHTTPClient(testCtx(t), ts.URL, testBearer, nil)
	if err != nil {
		t.Fatalf("connect: %v (code=%s)", err, ErrorCode(err))
	}
	t.Cleanup(func() { _ = s.Close() })

	got := map[string]bool{}
	for _, tool := range s.Tools() {
		got[tool.Name] = true
		if len(tool.InputSchema) == 0 {
			t.Errorf("tool %s has no live schema", tool.Name)
		}
	}
	// A newly enabled tool works with no client change.
	if len(got) != len(names) {
		t.Fatalf("exposed %d tools (%v), want the discovered set without the restricted ones", len(got), got)
	}
	for _, name := range names {
		if !got[name] {
			t.Errorf("missing discovered tool %q", name)
		}
	}
	res, err := s.Call(testCtx(t), "brand_new_group_tool", json.RawMessage(`{}`))
	if err != nil || res.IsError || res.Content != "ok" {
		t.Fatalf("new tool must be executable: res=%+v err=%v", res, err)
	}
	for _, name := range restrictedWordPressTools {
		if got[name] {
			t.Errorf("restricted tool %q was exposed", name)
		}
		if _, err := s.Call(testCtx(t), name, json.RawMessage(`{}`)); ErrorCode(err) != string(CodeInvalidTools) {
			t.Errorf("call %q code = %q, want invalid_tools", name, ErrorCode(err))
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("connect executed %d tools; discovery must never execute tools", n)
	}
}

func TestConnectWordPressAuthAndEndpointPolicy(t *testing.T) {
	ts := newRuneTestServer(t, testBearer, wordPressDef(echoOK("ok"), "get_content"), nil)
	if _, err := connectWordPressWithHTTPClient(testCtx(t), ts.URL, "wrong-token", nil); ErrorCode(err) != string(CodeUnauthorized) {
		t.Fatalf("code = %q, want unauthorized", ErrorCode(err))
	}
	// The same endpoint policy as Connect: no private or odd-port destinations.
	for _, ep := range []string{"https://127.0.0.1/mcp", "https://169.254.169.254/", "https://example.com:22/mcp", ""} {
		if _, err := ConnectWordPress(testCtx(t), ep, testBearer); ErrorCode(err) != string(CodeInvalidEndpoint) {
			t.Errorf("endpoint %q: code = %q, want invalid_endpoint", ep, ErrorCode(err))
		}
	}
	if _, err := ConnectWordPress(testCtx(t), "https://example.com/mcp", "bad token"); ErrorCode(err) != string(CodeInvalidToken) {
		t.Errorf("token code = %q, want invalid_token", ErrorCode(err))
	}
}

func TestDiscoverToolsKeepsDynamicNamesAndGroups(t *testing.T) {
	fl := &fakeLister{fn: func(cursor string) (*mcp.ListToolsResult, error) {
		page := validToolPage([]string{"get_content", "list_content", "batch", "sql_query", "future_capability_tool"}, "")
		page.Tools[4].Meta = mcp.Meta{"group": "experimental"}
		return page, nil
	}}
	tools, err := discoverTools(testCtx(t), fl)
	if err != nil {
		t.Fatalf("discoverTools: %v (code=%s)", err, ErrorCode(err))
	}
	got := map[string]Tool{}
	for _, tool := range tools {
		got[tool.Name] = tool
	}
	if len(got) != 3 {
		t.Fatalf("got %d tools (%v), want the three unrestricted names", len(got), got)
	}
	if got["future_capability_tool"].Group != "experimental" {
		t.Errorf("server capability group was not retained: %+v", got["future_capability_tool"])
	}
}

func TestDiscoverToolsAcceptsFullServerList(t *testing.T) {
	// The WordPress MCP server advertises 165 tools and keeps growing;
	// discovery must not trip its own advertised-tool bound.
	var advertised []string
	for i := 0; i < 400; i++ {
		advertised = append(advertised, fmt.Sprintf("unreviewed_tool_%03d", i))
	}
	fl := &fakeLister{fn: func(cursor string) (*mcp.ListToolsResult, error) {
		return validToolPage(advertised, ""), nil
	}}
	tools, err := discoverTools(testCtx(t), fl)
	if err != nil {
		t.Fatalf("discoverTools: %v (code=%s)", err, ErrorCode(err))
	}
	if len(tools) != len(advertised) {
		t.Fatalf("accepted %d tools, want all %d advertised", len(tools), len(advertised))
	}
	if MaxDiscoveredTools <= len(advertised) {
		t.Errorf("bound check: advertised=%d MaxDiscoveredTools=%d", len(advertised), MaxDiscoveredTools)
	}
}

func TestDiscoverToolsBoundsStillFailClosed(t *testing.T) {
	tooMany := make([]string, 0, MaxDiscoveredTools+1)
	for i := 0; i <= MaxDiscoveredTools; i++ {
		tooMany = append(tooMany, "unreviewed_"+strings.Repeat("y", i))
	}
	fl := &fakeLister{fn: func(cursor string) (*mcp.ListToolsResult, error) {
		return validToolPage(tooMany, ""), nil
	}}
	if _, err := discoverTools(testCtx(t), fl); ErrorCode(err) != string(CodeInvalidTools) {
		t.Fatalf("code = %q, want invalid_tools for over-advertised tools", ErrorCode(err))
	}
}

func TestConnectWordPressSucceedsWithNoUsableTools(t *testing.T) {
	// A server with no capability group enabled offers nothing usable; the
	// connection still succeeds and simply exposes no tools.
	ts := newRuneTestServer(t, testBearer, wordPressDef(echoOK("ok"), "batch", "sql_query", "read_file"), nil)
	s, err := connectWordPressWithHTTPClient(testCtx(t), ts.URL, testBearer, nil)
	if err != nil {
		t.Fatalf("connect: %v (code=%s)", err, ErrorCode(err))
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := s.Tools(); len(got) != 0 {
		t.Fatalf("got %d tools, want none", len(got))
	}
	if _, err := s.Call(testCtx(t), "get_content", json.RawMessage(`{}`)); ErrorCode(err) != string(CodeInvalidTools) {
		t.Fatalf("call code = %q, want invalid_tools", ErrorCode(err))
	}
}

func TestConnectWordPressRejectsMalformedProtocol(t *testing.T) {
	ts := newMCPGarbageServer(t)
	if s, err := connectWordPressWithHTTPClient(testCtx(t), ts, testBearer, nil); err == nil {
		t.Fatal("malformed protocol must be rejected")
	} else if s != nil {
		t.Fatal("no session may be returned for a malformed protocol")
	}
}

func TestConnectWordPressCloseIdempotent(t *testing.T) {
	ts := newRuneTestServer(t, testBearer, wordPressDef(echoOK("ok"), "get_content"), nil)
	s, err := connectWordPressWithHTTPClient(testCtx(t), ts.URL, testBearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := s.Call(testCtx(t), "get_content", json.RawMessage(`{"id":1}`)); ErrorCode(err) != string(CodeUnreachable) {
		t.Fatalf("call after close code = %q", ErrorCode(err))
	}
}
