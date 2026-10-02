package runecms

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestSafeDialPortGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, port := range []string{"80", "8080", "443", "8443"} {
		_, err := safeDialContext(ctx, "tcp", "127.0.0.1:"+port)
		if err == nil || err.Error() != "runecms: blocked host" {
			t.Errorf("127.0.0.1:%s: err = %v, want blocked-host (port must pass)", port, err)
		}
	}
	for _, port := range []string{"8081", "8090", "8000", "3000", "22"} {
		_, err := safeDialContext(ctx, "tcp", "127.0.0.1:"+port)
		if err == nil || err.Error() != "runecms: blocked port" {
			t.Errorf("127.0.0.1:%s: err = %v, want blocked-port", port, err)
		}
	}
}

// headerCapture records the headers bearerTransport actually sends.
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

// The outbound User-Agent must be ours even when the caller supplies Go's
// default, and the caller's own request must come back untouched.
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

// A firewall that rejects the default Go user agent must not block the
// WordPress connect path.
func TestConnectWordPressSendsProductUserAgent(t *testing.T) {
	inner := newRuneTestServer(t, testBearer, wordPressDef(echoOK("ok"), "get_content"), nil)
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

	s, err := connectWordPressWithHTTPClient(testCtx(t), ts.URL, testBearer, nil)
	if err != nil {
		t.Fatalf("connect: %v (code=%s)", err, ErrorCode(err))
	}
	t.Cleanup(func() { _ = s.Close() })
	if sawGoUA.Load() {
		t.Error("an outbound request still used the default Go user agent")
	}
	if len(s.Tools()) != 1 {
		t.Errorf("discovered %d tools, want the single profile tool", len(s.Tools()))
	}
}
