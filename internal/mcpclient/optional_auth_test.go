package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newRecordingServer serves the shared tool set while recording every
// request's headers, so tests prove exactly what an anonymous session sends.
func newRecordingServer(t *testing.T, mu *sync.Mutex, seen *[]http.Header) string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-test", Version: "0.0.1"}, nil)
	for _, d := range baseDefs(echoOK(`{"ok":true}`)) {
		d := d
		srv.AddTool(&mcp.Tool{Name: d.name, Description: "test " + d.name, InputSchema: d.schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"ok":true}`}}}, nil
			})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.Header.Clone())
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestConnectWithoutTokenDiscoversNoAuthServer proves an explicitly
// unauthenticated session lists tools, and that discovery still never
// executes one.
func TestConnectWithoutTokenDiscoversNoAuthServer(t *testing.T) {
	var calls atomic.Int64
	srv := newTestServer(t, "", baseDefs(echoOK(`{"ok":true}`)), &calls)
	session := mustConnect(t, testCtx(t), srv.URL, "")
	if len(session.Tools()) != len(baseDefs(echoOK(`{"ok":true}`))) {
		t.Fatalf("discovered %d tools, want the full advertised set", len(session.Tools()))
	}
	if calls.Load() != 0 {
		t.Fatalf("discovery executed %d tools, want none", calls.Load())
	}
}

// TestConnectWithoutTokenSendsNoAuthorization captures the wire headers of
// an anonymous session: no Authorization header may be sent, not even an
// empty "Bearer " value.
func TestConnectWithoutTokenSendsNoAuthorization(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	url := newRecordingServer(t, &mu, &seen)
	mustConnect(t, testCtx(t), url, "")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no requests were recorded during anonymous discovery")
	}
	for _, header := range seen {
		if auth := header.Get("Authorization"); auth != "" {
			t.Fatalf("anonymous session sent Authorization %q, want no header", auth)
		}
		if !strings.Contains(header.Get("User-Agent"), "revserp") {
			t.Fatalf("anonymous session lost the product User-Agent: %q", header.Get("User-Agent"))
		}
	}
}

// TestConnectWithTokenStillSendsBearer keeps the authenticated path intact:
// a supplied token gates a protected server and a wrong one fails closed.
func TestConnectWithTokenStillSendsBearer(t *testing.T) {
	var calls atomic.Int64
	srv := newTestServer(t, testBearer, baseDefs(echoOK(`{"ok":true}`)), &calls)
	session := mustConnect(t, testCtx(t), srv.URL, testBearer)
	if len(session.Tools()) == 0 {
		t.Fatal("authenticated discovery returned no tools")
	}
	if _, err := connectWithHTTPClient(testCtx(t), srv.URL, "wrong-token", nil); ErrorCode(err) != string(CodeUnauthorized) {
		t.Fatalf("wrong token code = %q, want unauthorized", ErrorCode(err))
	}
}

// TestConnectRejectsMalformedNonemptyToken keeps validation of supplied
// tokens strict: only the explicitly empty no-auth case is exempt.
func TestConnectRejectsMalformedNonemptyToken(t *testing.T) {
	srv := newTestServer(t, "", baseDefs(echoOK(`{"ok":true}`)), nil)
	for _, token := range []string{"has space", "line\nbreak", strings.Repeat("x", maxBearerLen+1)} {
		if _, err := connectWithHTTPClient(testCtx(t), srv.URL, token, nil); ErrorCode(err) != string(CodeInvalidToken) {
			t.Fatalf("token %q code = %q, want invalid_token", token, ErrorCode(err))
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestBearerTransportOmitsEmptyToken is the header-level contract behind
// anonymous discovery.
func TestBearerTransportOmitsEmptyToken(t *testing.T) {
	captured := map[string]string{}
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured["auth"] = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	mkreq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "https://mcp.example.test/mcp", nil)
		req.Header.Set("Authorization", "stale")
		return req
	}
	if _, err := (&bearerTransport{base: base, token: ""}).RoundTrip(mkreq()); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if captured["auth"] != "" {
		t.Fatalf("empty token sent Authorization %q, want no header", captured["auth"])
	}
	if _, err := (&bearerTransport{base: base, token: testBearer}).RoundTrip(mkreq()); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if captured["auth"] != "Bearer "+testBearer {
		t.Fatalf("nonempty token sent %q, want the bearer credential", captured["auth"])
	}
}

// TestCallWithoutTokenExecutesAnonymousTool proves an anonymous session can
// still call a discovered tool by its exact name (policy lives above this
// layer).
func TestCallWithoutTokenExecutesAnonymousTool(t *testing.T) {
	srv := newTestServer(t, "", baseDefs(echoOK(`{"ok":true}`)), nil)
	session := mustConnect(t, testCtx(t), srv.URL, "")
	result, err := session.Call(context.Background(), "list_collections", nil)
	if err != nil {
		t.Fatalf("anonymous call: %v (code=%s)", err, ErrorCode(err))
	}
	if result.IsError || !strings.Contains(result.Content, "ok") {
		t.Fatalf("anonymous call result = %+v, want success content", result)
	}
}
