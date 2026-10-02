// Package runecms is a reusable outbound client for a CMS MCP endpoint
// (Rune or WordPress).
//
// One Session serves one chat job; cancel the call context to abort.
// Connect performs MCP initialization plus tools/list pagination only and
// never executes tools. Every advertised tool is kept: names are checked
// against the dynamic name policy and the exposure exclusions, not against
// a static allowlist, so a new server tool or capability group works without
// a client release.
//
// Security properties (production path, Connect):
//
//   - HTTP/HTTPS public endpoints (web ports only). URL credentials,
//     fragments and unexpected ports are rejected, as are empty or
//     malformed bearer tokens. HTTP allows ports 80/8080, HTTPS allows
//     ports 443/8443.
//   - Private, loopback, link-local, multicast, unspecified, reserved,
//     documentation, benchmark and CGNAT addresses are blocked, including
//     IPv6 and IPv4-mapped forms. Hostnames are resolved before connecting
//     and every resolved address must be public; dialing re-resolves and
//     pins the connection to one validated IP, which stops DNS rebinding
//     between resolution and connect.
//   - The HTTP transport ignores proxy environment variables and refuses
//     redirects, so bearer tokens can never leak to a third host.
//   - Timeouts bound every connect and call; response/event sizes are
//     capped; tool results are capped or truncated.
//
// No-write-retry behavior (verified against go-sdk v1.8.0, see below):
//
//   - StreamableClientTransport is configured with MaxRetries: -1 (zero
//     reconnect attempts) and DisableStandaloneSSE: true, so there is no
//     background SSE stream to resume and no Last-Event-ID replay.
//   - No OAuthHandler is set, so Transport.Write issues exactly one POST
//     per call; the authorize-and-retry second POST path is dead.
//   - On session expiry the server answers 404 and the transport surfaces
//     ErrSessionMissing to the caller. It does not re-initialize and does
//     not re-send the call. Our Call performs exactly one CallTool attempt.
//   - Callers must create a fresh Session after an unreachable/session
//     error instead of retrying a write on a dead session.
//
// Errors are always typed: every failure is an *Error carrying a Code.
// Error.Error() returns only curated safe text (no endpoint URLs, tokens,
// or remote response bodies). Use ErrorCode to branch on failures.
package runecms

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Code is a stable, API-safe failure category. Never surface raw error
// strings to callers; branch on these codes instead.
type Code string

const (
	// CodeInvalidEndpoint covers malformed endpoints and endpoints rejected
	// by policy: non-HTTP(S) scheme, URL credentials, fragments, unexpected
	// ports, unresolvable hosts, and blocked (non-public) destinations.
	CodeInvalidEndpoint Code = "invalid_endpoint"
	// CodeInvalidToken covers missing, overlong, or malformed bearer tokens.
	CodeInvalidToken Code = "invalid_token"
	// CodeUnauthorized covers server 401/403 responses.
	CodeUnauthorized Code = "unauthorized"
	// CodeUnreachable covers network failures and dead/expired sessions.
	CodeUnreachable Code = "unreachable"
	// CodeUnsupportedTransport covers transport behavior we refuse: server
	// redirects, unexpected content types, and tool results whose content
	// has no readable form.
	CodeUnsupportedTransport Code = "unsupported_transport"
	// CodeInvalidTools covers discovery failures, unusable tool schemas,
	// invalid discovered names, and malformed tool arguments.
	CodeInvalidTools Code = "invalid_tools"
	// CodeTooLarge covers oversized arguments and oversized tool results.
	CodeTooLarge Code = "too_large"
	// CodeTimeout covers connect and call deadlines.
	CodeTimeout Code = "timeout"
)

// Error is the only error type this package returns. Error() renders a
// curated safe message; the underlying cause stays available via Unwrap
// for errors.Is/As (e.g. context.DeadlineExceeded) but is never printed.
type Error struct {
	Code  Code
	msg   string
	inner error
}

func (e *Error) Error() string { return "runecms " + string(e.Code) + ": " + e.msg }
func (e *Error) Unwrap() error { return e.inner }

func fail(code Code, msg string, inner error) *Error {
	return &Error{Code: code, msg: msg, inner: inner}
}

// ErrorCode reports the runecms failure category for err, unwrapping as
// needed. It returns "" for nil errors and for errors this package did
// not produce, except that context deadline errors map to "timeout".
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return string(e.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(CodeTimeout)
	}
	return ""
}

// Tool is one CMS tool discovered at connect time. Group is the server
// capability group when the server supplies one; it is display metadata.
type Tool struct {
	Name        string
	Description string
	Group       string
	InputSchema json.RawMessage
}

// Result is the outcome of one tool call. Tool-level failures arrive as
// Result values with IsError set, not as Go errors.
type Result struct {
	Content string
	IsError bool
}

const (
	connectTimeout = 15 * time.Second
	callTimeout    = 30 * time.Second
	resolveTimeout = 5 * time.Second
	dialTimeout    = 5 * time.Second

	maxBearerLen   = 8192
	maxArgsBytes   = 64 * 1024
	maxSchemaBytes = 64 * 1024
	maxSchemaProps = 128
	maxDescription = 4 * 1024

	// maxResultBytes caps marshaled structuredContent; oversized results
	// fail with CodeTooLarge rather than corrupt JSON by truncation.
	maxResultBytes = 128 * 1024
	// maxResultText caps text results; oversized text is truncated with a
	// marker, which is safe for display text.
	maxResultText = 64 * 1024

	// maxHTTPBodyBytes caps every HTTP response body (initialize, list,
	// call) at the transport layer. MaxEventSize only bounds SSE events,
	// so plain JSON bodies need their own limit.
	maxHTTPBodyBytes = 1 << 20

	// maxListPages and MaxDiscoveredTools bound discovery. The WordPress MCP
	// server ships 165 tools and keeps growing, so the ceilings sit well above
	// that and are still finite.
	maxListPages  = 20
	maxEventBytes = 1 << 20
	// maxToolName bounds one discovered name; it is also charset-checked.
	maxToolName = 128
)

// IsValidToolName reports whether name is a usable namespaced-provider tool
// suffix: bounded length, ASCII letters, digits, underscores and dashes, first
// character alphanumeric, and no doubled underscore, so a name can never
// smuggle a second namespace (cms__ / wp__) into a model-facing name. The
// aichattools name checks reuse it, so both providers accept the same dynamic
// identifiers.
func IsValidToolName(name string) bool {
	if name == "" || len(name) > maxToolName || strings.Contains(name, "__") {
		return false
	}
	first := name[0]
	if !isToolNameByte(first, true) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isToolNameByte(name[i], false) {
			return false
		}
	}
	return true
}

func isToolNameByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_' || c == '-':
		return !first
	}
	return false
}

// restrictedToolTokens keep an advertised tool out of every session whatever
// the server does: filesystem access, raw SQL / database access and batch
// execution, which would replay nested calls around the approval policy.
// These are exposure exclusions, separate from the approval policy.
var restrictedToolTokens = []string{"sql", "file", "batch", "shell", "exec"}

// restrictedToolExceptions are reviewed content tools whose names contain a
// restricted token and stay available.
var restrictedToolExceptions = map[string]bool{"batch_update_content": true}

// restrictedToolNames are restricted tools whose names do not decompose into
// the token vocabulary above: raw SQL schema access and theme file writes.
var restrictedToolNames = map[string]bool{"describe_tables": true, "create_child_theme": true}

// exposureExcluded reports whether a discovered name stays unavailable. It
// matches whole name parts, so "set_featured_image" survives while "read_file"
// and "run_sql" do not.
func exposureExcluded(name string) bool {
	lower := strings.ToLower(name)
	if restrictedToolNames[lower] {
		return true
	}
	if restrictedToolExceptions[lower] {
		return false
	}
	parts := strings.FieldsFunc(lower, func(r rune) bool { return r == '_' || r == '-' })
	for _, part := range parts {
		for _, token := range restrictedToolTokens {
			if part == token {
				return true
			}
		}
	}
	return false
}

// MaxDiscoveredTools bounds how many tool entries one server may advertise
// before discovery fails closed. It is exported so no other layer re-imposes a
// smaller cap of its own on a live session.
const MaxDiscoveredTools = 512

// ToolExposed reports whether a discovered name may reach the model and be
// executed: a valid name that is not a restricted exposure. Transport and the
// aichattools builders share it, so the two layers cannot drift.
func ToolExposed(name string) bool {
	return IsValidToolName(name) && !exposureExcluded(name)
}

// errRedirectRefused stops redirect following so bearer tokens stay on the
// configured endpoint.
var errRedirectRefused = errors.New("redirects are not allowed")

// errBodyTooLarge surfaces when an HTTP response body exceeds
// maxHTTPBodyBytes. It is matched with errors.Is to classify CodeTooLarge.
var errBodyTooLarge = errors.New("response body exceeds size limit")

// ---------------------------------------------------------------------------
// Endpoint and token validation
// ---------------------------------------------------------------------------

var blockedNets []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
		"192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			blockedNets = append(blockedNets, n)
		}
	}
}

// isPublicIP reports whether ip is a globally routable unicast address.
// IPv4-mapped IPv6 forms are normalized before checking.
func isPublicIP(ip net.IP) bool {
	if len(ip) == 0 {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

func validateBearer(token string) error {
	if token == "" {
		return fail(CodeInvalidToken, "bearer token is empty", nil)
	}
	if len(token) > maxBearerLen {
		return fail(CodeInvalidToken, "bearer token is too long", nil)
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c < 0x21 || c > 0x7e {
			return fail(CodeInvalidToken, "bearer token contains unsupported characters", nil)
		}
	}
	return nil
}

// validateStrictEndpoint enforces the production policy: HTTP/HTTPS on web
// ports only (HTTP: 80/8080, HTTPS: 443/8443), no credentials, no
// fragment, and a host that resolves exclusively to public addresses.
// DNS uses the caller's context bounded by
// resolveTimeout so validation never outlives the connect deadline.
func validateStrictEndpoint(ctx context.Context, endpoint string) (*url.URL, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint is empty", nil)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint is not a valid URL", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if !u.IsAbs() || (scheme != "https" && scheme != "http") {
		return nil, fail(CodeInvalidEndpoint, "endpoint must be an http(s) URL", nil)
	}
	if u.User != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not embed credentials", nil)
	}
	if u.Fragment != "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not contain a fragment", nil)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint has no host", nil)
	}
	port := u.Port()
	okPort := (scheme == "http" && (port == "" || port == "80" || port == "8080")) ||
		(scheme == "https" && (port == "" || port == "443" || port == "8443"))
	if !okPort {
		return nil, fail(CodeInvalidEndpoint, "endpoint uses an unexpected port", nil)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, fail(CodeInvalidEndpoint, "endpoint host is not allowed", nil)
		}
		return u, nil
	}
	rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(rctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fail(CodeUnreachable, "endpoint host cannot be resolved", err)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, fail(CodeInvalidEndpoint, "endpoint host is not allowed", nil)
		}
	}
	return u, nil
}

// validateTestEndpoint is the test-only relaxed check used with injected
// HTTP clients: http/https allowed, any port, but still no credentials or
// fragments. Production Connect never uses this path.
func validateTestEndpoint(endpoint string) (*url.URL, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint is empty", nil)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint is not a valid URL", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if !u.IsAbs() || (scheme != "https" && scheme != "http") {
		return nil, fail(CodeInvalidEndpoint, "endpoint must be an http(s) URL", nil)
	}
	if u.User != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not embed credentials", nil)
	}
	if u.Fragment != "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not contain a fragment", nil)
	}
	if u.Hostname() == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint has no host", nil)
	}
	return u, nil
}

// safeDialContext dials only validated public addresses. Hostnames are
// resolved here at dial time and the connection is pinned to one validated
// IP, so DNS cannot rebind between validation and connect. Any blocked
// address in the answer fails the dial closed.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	switch port {
	case "80", "8080", "443", "8443":
	default:
		return nil, errors.New("runecms: blocked port")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("runecms: DNS resolution returned no addresses")
		}
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, errors.New("runecms: blocked host")
		}
	}
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// productUserAgent identifies this client honestly and stably. Upstream
// firewalls answer Go's default Go-http-client/1.1 with 406 HTML.
const productUserAgent = "revserp-mcp/1.0"

// bearerTransport injects the static bearer token and the product user agent
// on every request, and caps every response body at maxHTTPBodyBytes. The
// SDK's MaxEventSize only bounds SSE events, so plain JSON bodies need this
// wrapper. Headers are set on a clone, so caller requests are untouched.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

// cappedBody fails reads with errBodyTooLarge once more than
// maxHTTPBodyBytes are available, so oversized JSON bodies surface as
// CodeTooLarge instead of buffering unbounded data.
type cappedBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.rc.Read(p)
	c.remaining -= int64(n)
	if c.remaining <= 0 && err == nil {
		return n, errBodyTooLarge
	}
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	out := r.Clone(r.Context())
	out.Header.Set("Authorization", "Bearer "+b.token)
	out.Header.Set("User-Agent", productUserAgent)
	resp, err := b.base.RoundTrip(out)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &cappedBody{rc: resp.Body, remaining: maxHTTPBodyBytes + 1}
	return resp, nil
}

// CloseIdleConnections forwards cleanup through the authentication wrapper.
func (b *bearerTransport) CloseIdleConnections() {
	if closer, ok := b.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return errRedirectRefused }

// newSafeHTTPClient builds the production HTTP client: pinned dialer,
// no proxy from the environment, TLS 1.2 minimum, bearer injection, and
// redirects refused.
func newSafeHTTPClient(token string) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport:     &bearerTransport{base: tr, token: token},
		CheckRedirect: refuseRedirect,
		Timeout:       50 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// Session is one Rune MCP conversation channel: exactly one per chat job.
// It is safe for concurrent Call use; Close is idempotent.
type Session struct {
	mu        sync.Mutex
	cs        *mcp.ClientSession
	tools     []Tool
	byName    map[string]Tool
	closed    bool
	closeHTTP func()
}

// Connect opens a CMS session: MCP initialization plus tools/list
// pagination. It never executes tools. Tools with an invalid name, a
// restricted exposure name, or an unusable object input schema are
// excluded; every other advertised tool is kept.
func Connect(ctx context.Context, endpoint, bearerToken string) (*Session, error) {
	if err := validateBearer(bearerToken); err != nil {
		return nil, err
	}
	// The whole connect path, including DNS validation, lives under one
	// deadline so a slow resolver cannot outlive the caller's context.
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if _, err := validateStrictEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	return connectInner(ctx, endpoint, newSafeHTTPClient(bearerToken), "rune")
}

// connectWithHTTPClient is the test-only injection point: tests pass a
// client that routes to a local httptest server, bypassing the production
// dial guard. It is unexported so production code cannot reach private
// networks through it.
func connectWithHTTPClient(ctx context.Context, endpoint, bearerToken string, hc *http.Client) (*Session, error) {
	if err := validateBearer(bearerToken); err != nil {
		return nil, err
	}
	if _, err := validateTestEndpoint(endpoint); err != nil {
		return nil, err
	}
	base := http.DefaultTransport
	var timeout time.Duration
	if hc != nil {
		if hc.Transport != nil {
			base = hc.Transport
		}
		timeout = hc.Timeout
	}
	cp := &http.Client{
		Transport:     &bearerTransport{base: base, token: bearerToken},
		CheckRedirect: refuseRedirect,
		Timeout:       timeout,
	}
	return connectInner(ctx, endpoint, cp, "rune")
}

// connectInner initializes one MCP session and keeps every discovered tool
// that passes the name and schema policy. It never executes a tool.
func connectInner(ctx context.Context, endpoint string, hc *http.Client, profile string) (*Session, error) {

	connected := false
	defer func() {
		if !connected {
			hc.CloseIdleConnections()
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: hc,
		// No automatic reconnect or stream resumption: every write is sent
		// at most once and session expiry surfaces as an error.
		MaxRetries: -1,
		// Stateless JSON mode: no persistent standalone SSE stream, so no
		// server-initiated replay channel exists on this client.
		DisableStandaloneSSE: true,
		MaxEventSize:         maxEventBytes,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "revserp-" + profile, Version: "1.0.0"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, classifyConnectError(ctx, err)
	}

	tools, err := discoverTools(ctx, cs)
	if err != nil {
		_ = cs.Close()
		return nil, err
	}
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	connected = true
	return &Session{cs: cs, tools: tools, byName: byName, closeHTTP: hc.CloseIdleConnections}, nil
}

// toolLister is the subset of *mcp.ClientSession used for discovery. It lets
// tests drive discoverTools with crafted pages without a network server.
type toolLister interface {
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
}

// discoverTools pages through tools/list and keeps every advertised tool
// whose name passes IsValidToolName and the exposure exclusions and whose
// input schema is a usable JSON object within property limits. Every
// advertised entry counts toward MaxDiscoveredTools, not just accepted ones;
// duplicate names and repeating cursors are rejected. An empty result is a
// valid session: a server with no capability group enabled offers nothing.
func discoverTools(ctx context.Context, cs toolLister) ([]Tool, error) {
	var out []Tool
	seen := map[string]struct{}{}
	seenCursors := map[string]struct{}{"": {}}
	advertised := 0
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, classifyConnectError(ctx, err)
		}
		if res == nil {
			return nil, fail(CodeInvalidTools, "tool discovery returned no result", nil)
		}
		for _, t := range res.Tools {
			if t == nil {
				continue
			}
			advertised++
			if advertised > MaxDiscoveredTools {
				return nil, fail(CodeInvalidTools, "server advertises too many tools", nil)
			}
			if _, dup := seen[t.Name]; dup {
				return nil, fail(CodeInvalidTools, "tool discovery returned duplicate tools", nil)
			}
			seen[t.Name] = struct{}{}
			if tool, ok := acceptTool(t); ok {
				out = append(out, tool)
			}
		}
		if res.NextCursor == "" {
			break
		}
		if _, loop := seenCursors[res.NextCursor]; loop {
			return nil, fail(CodeInvalidTools, "tool discovery did not terminate", nil)
		}
		seenCursors[res.NextCursor] = struct{}{}
		cursor = res.NextCursor
		if page == maxListPages-1 {
			return nil, fail(CodeInvalidTools, "tool discovery did not terminate", nil)
		}
	}
	return out, nil
}

// acceptTool keeps a discovered tool unless its name is invalid or excluded
// from exposure, or its input schema is unusable. The schema is additionally
// validated with jsonschema-go against its meta-schema; resolving uses no
// loader, so any remote $ref fails closed.
func acceptTool(t *mcp.Tool) (Tool, bool) {
	if !ToolExposed(t.Name) {
		return Tool{}, false
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil || len(raw) == 0 || len(raw) > maxSchemaBytes {
		return Tool{}, false
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return Tool{}, false
	}
	typ, _ := schema["type"].(string)
	if typ != "object" {
		return Tool{}, false
	}
	if props, ok := schema["properties"]; ok && props != nil {
		pm, ok := props.(map[string]any)
		if !ok || len(pm) > maxSchemaProps {
			return Tool{}, false
		}
	}
	var jschema jsonschema.Schema
	if err := json.Unmarshal(raw, &jschema); err != nil {
		return Tool{}, false
	}
	if _, err := jschema.Resolve(nil); err != nil {
		return Tool{}, false
	}
	desc := truncateUTF8(t.Description, maxDescription)
	schemaCopy := append(json.RawMessage(nil), raw...)
	return Tool{Name: t.Name, Description: desc, Group: toolGroup(t), InputSchema: schemaCopy}, true
}

// toolGroup reads the server capability group out of tool metadata when one
// is supplied. It is bounded display metadata and is never trusted.
func toolGroup(t *mcp.Tool) string {
	for _, key := range []string{"group", "capability_group", "category"} {
		if value, ok := t.Meta[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncateUTF8(strings.TrimSpace(value), maxToolName)
		}
	}
	return ""
}

// truncateUTF8 cuts s to at most max bytes without splitting a UTF-8
// sequence, so truncation never emits invalid encoding.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// Tools returns the tools discovered for this session.
func (s *Session) Tools() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tool, len(s.tools))
	for i, t := range s.tools {
		out[i] = Tool{
			Name:        t.Name,
			Description: t.Description,
			Group:       t.Group,
			InputSchema: append(json.RawMessage(nil), t.InputSchema...),
		}
	}
	return out
}

// Call executes one tool exactly once: no retries, so writes are never
// re-sent by this client. Successful calls prefer structuredContent
// (marshaled once) and fall back to text; tool errors arrive as Result
// with IsError set.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Result{}, fail(CodeUnreachable, "session is closed", nil)
	}
	cs := s.cs
	if _, ok := s.byName[name]; !ok {
		s.mu.Unlock()
		return Result{}, fail(CodeInvalidTools, "tool was not discovered for this session", nil)
	}
	s.mu.Unlock()

	argsMap, err := normalizeArgs(args)
	if err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: argsMap})
	if err != nil {
		return Result{}, classifyCallError(ctx, err)
	}
	if res == nil {
		return Result{}, fail(CodeInvalidTools, "tool returned no result", nil)
	}
	return renderResult(name, res)
}

func normalizeArgs(args json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]any{}, nil
	}
	if len(args) > maxArgsBytes {
		return nil, fail(CodeTooLarge, "tool arguments are too large", nil)
	}
	var m map[string]any
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fail(CodeInvalidTools, "tool arguments must be a JSON object", err)
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

// collectText joins text parts; nonText counts parts with no readable text.
func collectText(content []mcp.Content) (text string, nonText int) {
	var sb strings.Builder
	for _, c := range content {
		switch v := c.(type) {
		case *mcp.TextContent:
			if v != nil {
				sb.WriteString(v.Text)
			}
		default:
			nonText++
		}
	}
	return sb.String(), nonText
}

func capText(s string) string {
	if len(s) > maxResultText {
		return truncateUTF8(s, maxResultText) + "…[truncated]"
	}
	return s
}

// unsupportedNotice marks results that dropped unreadable content blocks so
// mixed text/unsupported payloads are never silently trimmed.
func unsupportedNotice(n int) string {
	return fmt.Sprintf("\n[notice: %d unsupported content block(s) omitted]", n)
}

func renderResult(tool string, res *mcp.CallToolResult) (Result, error) {
	if res.IsError {
		text, nonText := collectText(res.Content)
		if strings.TrimSpace(text) == "" {
			if len(res.Content) == 0 && res.StructuredContent == nil {
				return Result{Content: "tool failed without detail", IsError: true}, nil
			}
			return Result{}, fail(CodeUnsupportedTransport,
				"tool "+tool+" returned an error with unsupported content", nil)
		}
		out := capText(text)
		if nonText > 0 {
			out += unsupportedNotice(nonText)
		}
		return Result{Content: out, IsError: true}, nil
	}
	if res.StructuredContent != nil {
		_, nonText := collectText(res.Content)
		if nonText > 0 {
			// Structured output is returned verbatim as JSON and cannot
			// carry a notice marker; fail explicitly instead of dropping
			// blocks silently.
			return Result{}, fail(CodeUnsupportedTransport,
				"tool "+tool+" returned unsupported content alongside structured content", nil)
		}
		data, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return Result{}, fail(CodeInvalidTools, "tool "+tool+" returned unusable content", err)
		}
		if len(data) > maxResultBytes {
			return Result{}, fail(CodeTooLarge, "tool result exceeds size limit", nil)
		}
		if strings.TrimSpace(string(data)) == "" {
			return Result{}, fail(CodeInvalidTools, "tool "+tool+" returned an empty result", nil)
		}
		return Result{Content: string(data)}, nil
	}
	text, nonText := collectText(res.Content)
	if strings.TrimSpace(text) == "" {
		if nonText > 0 {
			return Result{}, fail(CodeUnsupportedTransport,
				"tool "+tool+" returned unsupported content", nil)
		}
		return Result{}, fail(CodeInvalidTools, "tool "+tool+" returned an empty result", nil)
	}
	out := capText(text)
	if nonText > 0 {
		out += unsupportedNotice(nonText)
	}
	return Result{Content: out}, nil
}

// Close terminates the session. It is idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cs := s.cs
	closeHTTP := s.closeHTTP
	s.mu.Unlock()
	if closeHTTP != nil {
		defer closeHTTP()
	}
	if cs == nil {
		return nil
	}
	if err := cs.Close(); err != nil {
		return fail(CodeUnreachable, "failed to close session", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Error classification: SDK/network errors become safe coded Errors.
// Only status-code words and our own operation labels are matched; remote
// bodies, URLs, and tokens never enter messages.
// ---------------------------------------------------------------------------

func isTimeoutErr(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return ctx.Err() == context.DeadlineExceeded
}

func isTooLargeErr(err error) bool {
	if errors.Is(err, errBodyTooLarge) {
		return true
	}
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), errBodyTooLarge.Error())
}

func isAuthErr(err error) bool {
	msg := err.Error()
	for _, s := range []string{"401", "Unauthorized", "unauthorized", "403", "Forbidden", "forbidden"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func classifyConnectError(ctx context.Context, err error) *Error {
	switch {
	case isTimeoutErr(ctx, err):
		return fail(CodeTimeout, "connect timed out", err)
	case isTooLargeErr(err):
		return fail(CodeTooLarge, "rune response exceeds size limit", err)
	case errors.Is(err, errRedirectRefused):
		return fail(CodeUnsupportedTransport, "server redirect was refused", err)
	case isAuthErr(err):
		return fail(CodeUnauthorized, "rune endpoint rejected credentials", err)
	default:
		return fail(CodeUnreachable, "cannot reach rune endpoint", err)
	}
}

func classifyCallError(ctx context.Context, err error) *Error {
	switch {
	case isTimeoutErr(ctx, err):
		return fail(CodeTimeout, "tool call timed out", err)
	case isTooLargeErr(err):
		return fail(CodeTooLarge, "tool result exceeds size limit", err)
	case errors.Is(err, errRedirectRefused):
		return fail(CodeUnsupportedTransport, "server redirect was refused", err)
	case isAuthErr(err):
		return fail(CodeUnauthorized, "rune endpoint rejected credentials", err)
	case errors.Is(err, mcp.ErrSessionMissing):
		return fail(CodeUnreachable, "rune session expired; open a new session", err)
	default:
		return fail(CodeUnreachable, "tool call did not complete", err)
	}
}
