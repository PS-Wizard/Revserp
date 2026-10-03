// Package mcpclient dials any remote HTTP MCP server with a user-supplied
// bearer token. It is provider-neutral: one connect path, one discovered-tool
// model, no per-service profile and no handshake beyond the MCP protocol.
//
// Connect initializes and lists tools; it never executes one. It applies
// protocol and resource bounds, and no user permission policy: the caller
// decides what a discovered tool may do.
//
// The endpoint is user-supplied and untrusted, so the production path enforces:
//
//   - HTTP/HTTPS on web ports only (80/8080, 443/8443). URL credentials,
//     fragments, other ports and empty, overlong or non-printable tokens are
//     rejected.
//   - Private, loopback, link-local, CGNAT, multicast, unspecified, reserved,
//     documentation, benchmark and IPv6-transition addresses are blocked in both
//     families, including IPv4-mapped forms. safeDialContext re-resolves at dial
//     time and pins one validated IP, which is what closes the DNS rebinding
//     window between validation and connect.
//   - Proxies from the environment are ignored and redirects are refused, so a
//     token cannot reach a third host.
//   - A write is sent at most once. MaxRetries is -1 and DisableStandaloneSSE is
//     true, so no stream is resumed; with no OAuthHandler the SDK's
//     authorize-and-retry POST is unreachable. A write of unknown outcome is
//     never replayed: open a fresh Session instead of retrying a dead one.
//   - Response, event, schema and result sizes are capped, and every failure is
//     a typed *Error whose message carries no URL, token or remote body.
//
// Remote names, descriptions, schemas and output are untrusted data. Nothing
// here interprets them as instructions.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Code is a stable, API-safe failure category. Never surface raw error strings
// to callers; branch on these codes instead.
type Code string

const (
	// CodeInvalidEndpoint covers malformed endpoints and endpoints rejected by
	// policy: non-HTTP(S) scheme, URL credentials, fragments, unexpected ports,
	// unresolvable hosts, and blocked (non-public) destinations.
	CodeInvalidEndpoint Code = "invalid_endpoint"
	// CodeInvalidToken covers missing, overlong, or malformed bearer tokens.
	CodeInvalidToken Code = "invalid_token"
	// CodeUnauthorized covers server 401/403 responses.
	CodeUnauthorized Code = "unauthorized"
	// CodeUnreachable covers network failures and dead/expired sessions.
	CodeUnreachable Code = "unreachable"
	// CodeUnsupportedTransport covers transport behavior we refuse: server
	// redirects, unexpected content types, and tool results whose content has no
	// readable form.
	CodeUnsupportedTransport Code = "unsupported_transport"
	// CodeInvalidTools covers discovery failures, unusable tool schemas, invalid
	// discovered names, and malformed tool arguments.
	CodeInvalidTools Code = "invalid_tools"
	// CodeTooLarge covers oversized arguments and oversized tool results.
	CodeTooLarge Code = "too_large"
	// CodeTimeout covers connect and call deadlines.
	CodeTimeout Code = "timeout"
)

// Error is the only error type this package returns. Error() renders a curated
// safe message; the underlying cause stays available via Unwrap for
// errors.Is/As (e.g. context.DeadlineExceeded) but is never printed.
type Error struct {
	Code  Code
	msg   string
	inner error
}

func (e *Error) Error() string { return "mcpclient " + string(e.Code) + ": " + e.msg }
func (e *Error) Unwrap() error { return e.inner }

func fail(code Code, msg string, inner error) *Error {
	return &Error{Code: code, msg: msg, inner: inner}
}

// ErrorCode reports the failure category for err, unwrapping as needed. It
// returns "" for nil errors and for errors this package did not produce, except
// that context deadline errors map to "timeout".
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

// Tool is one discovered tool. Group is the server's own capability group when
// it supplies one: untrusted display metadata with no meaning here.
type Tool struct {
	Name        string
	Description string
	Group       string
	InputSchema json.RawMessage
}

// Result is the outcome of one tool call. Tool-level failures arrive as Result
// values with IsError set, not as Go errors.
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

	// maxResultBytes caps marshaled structuredContent; oversized results fail
	// with CodeTooLarge rather than corrupt JSON by truncation.
	maxResultBytes = 128 * 1024
	// maxResultText caps text results; oversized text is truncated with a
	// marker, which is safe for display text.
	maxResultText = 64 * 1024

	// maxHTTPBodyBytes caps every HTTP response body (initialize, list, call) at
	// the transport layer. MaxEventSize only bounds SSE events, so plain JSON
	// bodies need their own limit.
	maxHTTPBodyBytes = 1 << 20

	// maxListPages and MaxDiscoveredTools bound discovery so a hostile or broken
	// server cannot stream pages forever.
	maxListPages  = 20
	maxEventBytes = 1 << 20
	// maxToolName bounds one discovered name; it is also charset-checked.
	maxToolName = 128
)

// MaxDiscoveredTools bounds how many tool entries one server may advertise
// before discovery fails closed. It is exported so no other layer re-imposes a
// smaller cap of its own on a live session.
const MaxDiscoveredTools = 512

// IsValidToolName reports whether name is a usable MCP tool name, using the
// go-sdk grammar: non-empty, at most maxToolName bytes, drawn from
// [A-Za-z0-9_.-]. Dots and doubled underscores are legal, so exact names survive
// discovery; model aliases are the caller's to build from a name digest.
func IsValidToolName(name string) bool {
	if name == "" || len(name) > maxToolName {
		return false
	}
	for _, r := range name {
		if !isToolNameRune(r) {
			return false
		}
	}
	return true
}

func isToolNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		r == '_' || r == '-' || r == '.'
}

// Session is one remote MCP conversation channel: exactly one per connection
// per turn. It is safe for concurrent Call use; Close is idempotent.
type Session struct {
	mu        sync.Mutex
	cs        *mcp.ClientSession
	tools     []Tool
	byName    map[string]Tool
	closed    bool
	closeHTTP func()
}

// Connect opens one session: MCP initialization plus tools/list pagination. It
// never executes a tool. endpoint must be a public HTTP(S) URL; token is a
// bearer credential or empty for explicitly unauthenticated servers, and an
// empty token sends no Authorization header. Both are validated before any
// request is sent. Callers decide whether no-auth is permitted.
func Connect(ctx context.Context, endpoint, token string) (*Session, error) {
	if err := validateBearerOptional(token); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if _, err := validateStrictEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	return connectInner(ctx, endpoint, newSafeHTTPClient(token))
}

// connectWithHTTPClient is the test-only injection point: tests pass a client
// routed to a local httptest server. Unexported so production code cannot reach
// private networks through it.
func connectWithHTTPClient(ctx context.Context, endpoint, token string, hc *http.Client) (*Session, error) {
	if err := validateBearerOptional(token); err != nil {
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
		Transport:     &bearerTransport{base: base, token: token},
		CheckRedirect: refuseRedirect,
		Timeout:       timeout,
	}
	return connectInner(ctx, endpoint, cp)
}

func connectInner(ctx context.Context, endpoint string, hc *http.Client) (*Session, error) {
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
		// No automatic reconnect or stream resumption: every write is sent at
		// most once and session expiry surfaces as an error.
		MaxRetries: -1,
		// Stateless JSON mode: no persistent standalone SSE stream, so no
		// server-initiated replay channel exists on this client.
		DisableStandaloneSSE: true,
		MaxEventSize:         maxEventBytes,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "revserp", Version: "1.0.0"}, nil)
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

// toolLister lets tests drive discoverTools with crafted pages, no network.
type toolLister interface {
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
}

// discoverTools pages tools/list and keeps tools with a valid name and a usable
// object schema within property limits. Every advertised entry counts toward
// MaxDiscoveredTools, not just accepted ones, and duplicate names or a repeating
// cursor fail closed. An empty result is a valid session.
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

// acceptTool drops a tool with an invalid name or unusable input schema. It is
// protocol and resource hygiene only and makes no judgement about whether a
// tool is safe to run. The schema is also resolved against its meta-schema with
// no loader, so any remote $ref fails closed.
func acceptTool(t *mcp.Tool) (Tool, bool) {
	if !IsValidToolName(t.Name) {
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

// toolGroup returns the server's own capability group, bounded, and never trusted.
func toolGroup(t *mcp.Tool) string {
	for _, key := range []string{"group", "capability_group", "category"} {
		if value, ok := t.Meta[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncateUTF8(strings.TrimSpace(value), maxToolName)
		}
	}
	return ""
}

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

// Call executes one tool exactly once: no retries, so a write is never re-sent.
// Only a name discovered on this session may be called. Successful calls prefer
// structuredContent and fall back to text; tool errors arrive as Result with
// IsError set rather than as a Go error.
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
	if len(args) > maxArgsBytes {
		return nil, fail(CodeTooLarge, "tool arguments are too large", nil)
	}
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]any{}, nil
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

// unsupportedNotice marks dropped blocks so a mixed payload is never trimmed.
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
			// Structured output is returned verbatim as JSON and cannot carry a
			// notice marker; fail explicitly instead of dropping blocks silently.
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

// Only status-code words and our own operation labels are matched below; remote
// bodies, URLs and tokens never enter an error message.

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
		return fail(CodeTooLarge, "mcp response exceeds size limit", err)
	case errors.Is(err, errRedirectRefused):
		return fail(CodeUnsupportedTransport, "server redirect was refused", err)
	case isAuthErr(err):
		return fail(CodeUnauthorized, "mcp endpoint rejected credentials", err)
	default:
		return fail(CodeUnreachable, "cannot reach mcp endpoint", err)
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
		return fail(CodeUnauthorized, "mcp endpoint rejected credentials", err)
	case errors.Is(err, mcp.ErrSessionMissing):
		return fail(CodeUnreachable, "mcp session expired; open a new session", err)
	default:
		return fail(CodeUnreachable, "tool call did not complete", err)
	}
}
