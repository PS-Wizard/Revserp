package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/mcpclient"
)

const mcpTestEncryptionSecret = "mcp-test-encryption-secret-not-a-real-secret"

type fakeMCPSession struct {
	tools  []mcpclient.Tool
	closed *bool
}

func (s *fakeMCPSession) Tools() []mcpclient.Tool { return s.tools }
func (s *fakeMCPSession) Close() error {
	if s.closed != nil {
		*s.closed = true
	}
	return nil
}

func mcpHandlerRequest(t *testing.T, method, projectID, body string, userID pgtype.UUID) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/projects/"+projectID+"/mcp/connections", reader)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", projectID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

func TestParseMCPService(t *testing.T) {
	for _, value := range []string{"wordpress", "custom", " WordPress ", "CUSTOM"} {
		if _, ok := parseMCPService(value); !ok {
			t.Errorf("service %q rejected", value)
		}
	}
	for _, value := range []string{"", "rune", "drupal", "cms", "wp"} {
		if _, ok := parseMCPService(value); ok {
			t.Errorf("service %q accepted", value)
		}
	}
}

func TestValidateMCPName(t *testing.T) {
	if name, ok := validateMCPName("  WordPress  "); !ok || name != "WordPress" {
		t.Errorf("padded name = %q, %v", name, ok)
	}
	for _, value := range []string{"", "   ", strings.Repeat("x", mcpMaxNameLen+1)} {
		if _, ok := validateMCPName(value); ok {
			t.Errorf("name %q accepted", value)
		}
	}
}

func TestValidateMCPEndpoint(t *testing.T) {
	for _, value := range []string{"https://wp.example/mcp", "http://localhost:8080/x"} {
		if _, ok := validateMCPEndpoint(value); !ok {
			t.Errorf("endpoint %q rejected", value)
		}
	}
	for _, value := range []string{"", "ftp://x.example", "not a url", "https://", strings.Repeat("x", mcpMaxEndpointLen+1)} {
		if _, ok := validateMCPEndpoint(value); ok {
			t.Errorf("endpoint %q accepted", value)
		}
	}
}

func TestNormalizeMCPToolsStoresExactNames(t *testing.T) {
	raw, stored, err := normalizeMCPTools([]mcpclient.Tool{
		{Name: "list_content", Description: "Server text.", Group: "content", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "brand_new_thing", Description: "Server supplied.", Group: "experimental", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(stored) != 2 || stored[0].Name != "list_content" || stored[1].Group != "experimental" {
		t.Fatalf("stored = %+v, want exact remote names kept", stored)
	}
	roundTrip, err := parseMCPStoredTools(raw)
	if err != nil {
		t.Fatalf("parse stored: %v", err)
	}
	if len(roundTrip) != 2 || roundTrip[0].Name != "list_content" || roundTrip[0].Description != "Server text." {
		t.Fatalf("round trip = %+v", roundTrip)
	}
}

func TestNormalizeMCPToolsRejectsBadRemoteData(t *testing.T) {
	object := json.RawMessage(`{"type":"object"}`)
	for name, tools := range map[string][]mcpclient.Tool{
		"empty name":     {{Name: "", Description: "d", InputSchema: object}},
		"duplicate":      {{Name: "a", Description: "d", InputSchema: object}, {Name: "a", Description: "d", InputSchema: object}},
		"invalid name":   {{Name: "has space", Description: "d", InputSchema: object}},
		"bad schema":     {{Name: "a", Description: "d", InputSchema: json.RawMessage(`{oops}`)}},
		"oversize group": {{Name: "a", Description: "d", Group: strings.Repeat("x", mcpMaxToolGroupLen+1), InputSchema: object}},
	} {
		if _, _, err := normalizeMCPTools(tools); mcpErrorCode(err) != "invalid_tools" {
			t.Errorf("%s accepted", name)
		}
	}
	many := make([]mcpclient.Tool, 0, mcpMaxTools+1)
	for i := 0; i <= mcpMaxTools; i++ {
		many = append(many, mcpclient.Tool{Name: "tool_" + strings.Repeat("a", 10) + string(rune('a'+i%26)) + string(rune('0'+i/26)), Description: "d", InputSchema: object})
	}
	if _, _, err := normalizeMCPTools(many); mcpErrorCode(err) != "invalid_tools" {
		t.Error("oversized discovery accepted")
	}
}

func TestMCPConnectionJSONDefaultsAskAndHidesSecrets(t *testing.T) {
	now := pgtype.Timestamptz{}
	_ = now.Scan(time.Now())
	conn := sqlc.ProjectMcpConnection{
		Name: "WordPress", Service: "wordpress", EndpointUrl: "https://wp.example/mcp",
		EncryptedToken: "must-never-appear",
		Tools:          []byte(`[{"name":"update_content","description":"d","group":"content","input_schema":{}}]`),
		LastCheckedAt:  now,
	}
	rendered := newMCPConnectionJSON(conn, []mcpStoredTool{{Name: "update_content", Description: "d", Group: "content"}}, map[string]string{
		"update_content": "allow",
	})
	if len(rendered.Tools) != 1 || rendered.Tools[0].Permission != "allow" || !rendered.Tools[0].Available {
		t.Fatalf("tools = %+v", rendered.Tools)
	}
	absent := newMCPConnectionJSON(conn, []mcpStoredTool{{Name: "new_tool", Description: "d"}}, map[string]string{})
	if absent.Tools[0].Permission != "ask" {
		t.Fatalf("missing permission row = %q, want ask", absent.Tools[0].Permission)
	}
	raw, err := json.Marshal(rendered)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"token", "cipher", "secret", "bearer"} {
		if strings.Contains(strings.ToLower(string(raw)), needle) {
			t.Errorf("connection body mentions %q: %s", needle, raw)
		}
	}
}

func TestMCPConnectFailureMapping(t *testing.T) {
	for code, message := range map[string]string{
		"invalid_endpoint":      "invalid mcp endpoint",
		"invalid_token":         "invalid mcp credentials",
		"unauthorized":          "invalid mcp credentials",
		"unsupported_transport": "unsupported mcp endpoint",
		"unreachable":           "mcp endpoint unreachable",
		"timeout":               "mcp endpoint timed out",
		"too_large":             "mcp response too large",
		"invalid_tools":         "invalid mcp tools response",
		"bogus":                 "failed to reach mcp endpoint",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		mcpConnectFailure(rec, req, mcpCodedError{code: code})
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("code %q: decode: %v", code, err)
		}
		if body["error"] != message {
			t.Errorf("code %q: message = %q, want %q", code, body["error"], message)
		}
	}
}

func TestReadStrictJSONRejectsUnknownFields(t *testing.T) {
	var body mcpCreateConnectionRequest
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","service":"wordpress","endpoint_url":"https://x.example","bearer_token":"t","extra":1}`))
	if readStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
		t.Error("unknown fields accepted")
	}
}

func TestReadStrictJSONRejectsTrailingAndNull(t *testing.T) {
	var body mcpCreateConnectionRequest
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"} {"trailing":true}`))
	if readStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
		t.Error("trailing JSON accepted")
	}
	nullReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`null`))
	if readStrictJSONOrRespond(httptest.NewRecorder(), nullReq, &body) {
		t.Error("null literal accepted")
	}
}

func TestReadOptionalStrictJSONCoversCheckDeleteContract(t *testing.T) {
	var body struct{}
	empty := httptest.NewRequest(http.MethodPost, "/", nil)
	empty.Body = nil
	if !readOptionalStrictJSONOrRespond(httptest.NewRecorder(), empty, &body) {
		t.Error("empty check body rejected")
	}
	bad := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"unknown":true}`))
	if readOptionalStrictJSONOrRespond(httptest.NewRecorder(), bad, &body) {
		t.Error("unknown check fields accepted")
	}
}
