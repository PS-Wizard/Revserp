package mcpclient

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStrictEndpointHTTPSchemeAndPorts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pub := "93.184.216.34"
	good := []string{
		"http://" + pub + "/mcp",
		"http://" + pub + ":80/mcp",
		"http://" + pub + ":8080/mcp",
		"https://" + pub + "/mcp",
		"https://" + pub + ":443/mcp",
		"https://" + pub + ":8443/mcp",
	}
	for _, ep := range good {
		if _, err := validateStrictEndpoint(ctx, ep); err != nil {
			t.Errorf("endpoint %q: unexpected reject: %v (code=%s)", ep, err, ErrorCode(err))
		}
	}
	bad := []string{
		"ftp://" + pub + "/mcp",
		"ws://" + pub + "/mcp",
		"http://" + pub + ":443/mcp",
		"http://" + pub + ":8443/mcp",
		"http://" + pub + ":8081/mcp",
		"http://" + pub + ":8090/mcp",
		"https://" + pub + ":80/mcp",
		"https://" + pub + ":8080/mcp",
		"https://" + pub + ":8081/mcp",
		"http://user:pass@" + pub + "/mcp",
		"http://" + pub + "/mcp#frag",
		"http://10.0.0.1/mcp",
		"http://127.0.0.1/mcp",
		"http://169.254.169.254/",
		"http://[::1]/",
		"http://[fe80::1]/",
		"http://[ff02::1]/",
		"http://224.0.0.1/",
		"http://203.0.113.1/",
		"http://100.64.0.1/",
		"http://[::ffff:127.0.0.1]/",
	}
	for _, ep := range bad {
		if _, err := validateStrictEndpoint(ctx, ep); ErrorCode(err) != string(CodeInvalidEndpoint) {
			t.Errorf("endpoint %q: code = %q, want invalid_endpoint", ep, ErrorCode(err))
		}
	}
}

// public Connect entry point, so no test-only path can reach a private network.
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
	// An empty token is an explicitly unauthenticated session, so the
	// transport accepts it and any reachable failure surfaces from the
	// endpoint or the server instead. Whether no-auth is permitted is the
	// caller's policy (WordPress creation, patch, check and the turn
	// worker still require a credential there).
	for _, tok := range []string{"has space", "line\nbreak", "tab\there", strings.Repeat("a", maxBearerLen+1)} {
		if _, err := Connect(ctx, "https://example.com/mcp", tok); ErrorCode(err) != string(CodeInvalidToken) {
			t.Errorf("token %q: code = %q, want invalid_token", tok, ErrorCode(err))
		}
	}
}

func TestSafeDialPortGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, port := range []string{"80", "8080", "443", "8443"} {
		_, err := safeDialContext(ctx, "tcp", "127.0.0.1:"+port)
		if err == nil || err.Error() != "mcpclient: blocked host" {
			t.Errorf("127.0.0.1:%s: err = %v, want blocked-host (port must pass)", port, err)
		}
	}
	for _, port := range []string{"8081", "8090", "8000", "3000", "22"} {
		_, err := safeDialContext(ctx, "tcp", "127.0.0.1:"+port)
		if err == nil || err.Error() != "mcpclient: blocked port" {
			t.Errorf("127.0.0.1:%s: err = %v, want blocked-port", port, err)
		}
	}
}

// TestSafeDialEmptyDNSIsNonNilError guards the fail-closed path: .invalid never
// resolves, so the dial must fail with a non-nil error rather than index an
// empty address list.
func TestSafeDialEmptyDNSIsNonNilError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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

type headerCapture struct {
	got http.Header
}

func (c *headerCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	c.got = r.Header.Clone()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    r,
	}, nil
}

// TestBearerTransportSendsProductUserAgent pins that the outbound User-Agent is
// ours even when the caller supplies Go's default, and that the caller's own
// request comes back untouched.
func TestBearerTransportSendsProductUserAgent(t *testing.T) {
	cap := &headerCapture{}
	tr := &bearerTransport{base: cap, token: testBearer}
	req, err := http.NewRequestWithContext(testCtx(t), http.MethodPost, "https://example.com/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "Go-http-client/1.1")
	req.Header.Set("X-Caller", "keep")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := cap.got.Get("User-Agent"); got != productUserAgent {
		t.Errorf("outgoing User-Agent = %q, want %q", got, productUserAgent)
	}
	if got := cap.got.Get("Authorization"); got != "Bearer "+testBearer {
		t.Errorf("outgoing Authorization = %q, want the bearer token", got)
	}
	if got := cap.got.Get("X-Caller"); got != "keep" {
		t.Errorf("caller header dropped: X-Caller = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "Go-http-client/1.1" {
		t.Errorf("caller request modified: User-Agent = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("caller request gained Authorization = %q", got)
	}
}

// TestConnectSendsProductUserAgent proves it end to end behind a firewall that
// rejects Go's default user agent with 406.
func TestConnectSendsProductUserAgent(t *testing.T) {
	inner := newTestServer(t, testBearer, []toolDef{{name: "get_content", schema: objSchema(""), handler: echoOK("ok")}}, nil)
	u, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	var sawGoUA atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("User-Agent"), "Go-http-client/") {
			sawGoUA.Store(true)
			http.Error(w, "firewall", http.StatusNotAcceptable)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	s := mustConnect(t, testCtx(t), ts.URL, testBearer)
	if sawGoUA.Load() {
		t.Error("an outbound request still used the default Go user agent")
	}
	if len(s.Tools()) != 1 {
		t.Errorf("discovered %d tools, want 1", len(s.Tools()))
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

// TestToolGroupIsUntrustedDisplayMetadata proves a server-supplied group string
// is carried through bounded and never interpreted by this package.
func TestToolGroupIsUntrustedDisplayMetadata(t *testing.T) {
	tool, ok := acceptTool(&mcp.Tool{
		Name:        "list_collections",
		Description: "d",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Meta:        map[string]any{"group": strings.Repeat("g", maxToolName+50)},
	})
	if !ok {
		t.Fatal("valid tool rejected")
	}
	if len(tool.Group) > maxToolName {
		t.Fatalf("group %d bytes exceeds limit", len(tool.Group))
	}
}
