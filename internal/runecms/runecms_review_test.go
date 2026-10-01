package runecms

// Regression tests for the review fixes: bounded HTTP bodies, caller-scoped
// DNS, IPv6 transition blocks, UTF-8-safe truncation, discovery accounting,
// mixed unsupported content, and real JSON Schema validation.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
	ts := newRuneTestServer(t, testBearer, sixDefs(echoOK(big)), nil)
	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	_, err := s.Call(testCtx(t), "list_collections", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected oversized body error")
	}
	if got := ErrorCode(err); got != string(CodeTooLarge) {
		t.Fatalf("code = %q, want too_large", got)
	}
}

func TestSafeDialEmptyDNSIsNonNilError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10_000_000_000)
	defer cancel()
	// .invalid never resolves; the dial must fail closed with a non-nil
	// error and must never index an empty address list.
	_, err := safeDialContext(ctx, "tcp", "nonexistent.invalid:443")
	if err == nil {
		t.Fatal("expected non-nil DNS failure")
	}
}

func TestTransitionAddressesBlocked(t *testing.T) {
	for _, ip := range []string{
		"2001::1",                              // Teredo
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo example
		"2002:cb00:7101::",                     // 6to4
		"64:ff9b:1::1",                         // NAT64 local-use
		"64:ff9b::c000:201",                    // NAT64 well-known
	} {
		if isPublicIP(net.ParseIP(ip)) {
			t.Errorf("transition address %s treated as public", ip)
		}
	}
	ctx := testCtx(t)
	for _, ep := range []string{
		"https://[2001::1]/",
		"https://[2002:cb00:7101::]/",
		"https://[64:ff9b:1::1]/",
	} {
		if _, err := Connect(ctx, ep, testBearer); ErrorCode(err) != string(CodeInvalidEndpoint) {
			t.Errorf("endpoint %q: code = %q, want invalid_endpoint", ep, ErrorCode(err))
		}
	}
}

func TestUTF8SafeTruncation(t *testing.T) {
	desc := strings.Repeat("é", maxDescription+100) // 2-byte rune
	tool, ok := acceptTool(&mcp.Tool{
		Name:        "list_collections",
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if !ok {
		t.Fatal("valid tool rejected")
	}
	if !utf8.ValidString(tool.Description) {
		t.Fatal("description truncation produced invalid UTF-8")
	}
	if len(tool.Description) > maxDescription {
		t.Fatalf("description %d bytes exceeds limit", len(tool.Description))
	}
	out := capText(strings.Repeat("中", maxResultText+100)) // 3-byte rune
	if !utf8.ValidString(out) {
		t.Fatal("capText produced invalid UTF-8")
	}
	if !strings.HasSuffix(out, "…[truncated]") {
		t.Fatal("capText lost truncation marker")
	}
}

// fakeLister drives discoverTools with crafted pages.
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
	evil := make([]string, 0, maxListedTools+1)
	for i := 0; i < maxListedTools+1; i++ {
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

func TestDiscoveryRejectsDuplicateKnownNames(t *testing.T) {
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
