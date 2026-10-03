package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/mcpclient"
)

type mcpCapturedDial struct {
	endpoint string
	token    string
	tools    []mcpclient.Tool
	err      error
}

func mcpNoAuthTestTools() []mcpclient.Tool {
	return []mcpclient.Tool{
		{Name: "search_docs", Description: "Search the public documentation.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "fetch_page", Description: "Fetch one public documentation page.", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func mcpConnectionRequest(t *testing.T, method, projectID, connectionID, body string, userID pgtype.UUID) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", projectID)
	if connectionID != "" {
		routeContext.URLParams.Add("connectionID", connectionID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

func decodeMCPConnection(t *testing.T, rr *httptest.ResponseRecorder) mcpConnectionJSON {
	t.Helper()
	var decoded struct {
		Connection mcpConnectionJSON `json:"connection"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode connection response: %v (body %q)", err, rr.Body.String())
	}
	return decoded.Connection
}

func TestMCPCatalogListsCuratedPresets(t *testing.T) {
	fix := newMCPApprovalFixture(t)
	req := mcpConnectionRequest(t, http.MethodGet, fix.projectID.String(), "", "", fix.initiatorID)
	rr := httptest.NewRecorder()
	fix.app.handleMCPCatalog(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
	var decoded struct {
		Items []mcpCatalogItemJSON `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	want := map[string]mcpCatalogItemJSON{
		"wordpress":       {ID: "wordpress", Service: "wordpress"},
		"deepwiki":        {ID: "deepwiki", Service: "custom"},
		"cloudflare-docs": {ID: "cloudflare-docs", Service: "custom"},
		"microsoft-learn": {ID: "microsoft-learn", Service: "custom"},
		"se-ranking":      {ID: "se-ranking", Service: "custom"},
		"sanity":          {ID: "sanity", Service: "custom"},
		"windsor-ai":      {ID: "windsor-ai", Service: "custom"},
		"ahrefs":          {ID: "ahrefs", Service: "custom"},
		"storyblok":       {ID: "storyblok", Service: "custom"},
		"semrush":         {ID: "semrush", Service: "custom"},
		"accuranker":      {ID: "accuranker", Service: "custom"},
	}
	if len(decoded.Items) != len(want) {
		t.Fatalf("catalog has %d items, want %d", len(decoded.Items), len(want))
	}
	wantEndpoints := map[string]string{
		"deepwiki":        "https://mcp.deepwiki.com/mcp",
		"cloudflare-docs": "https://docs.mcp.cloudflare.com/mcp",
		"microsoft-learn": "https://learn.microsoft.com/api/mcp",
		"se-ranking":      "https://api.seranking.com/mcp",
		"sanity":          "https://mcp.sanity.io",
		"windsor-ai":      "https://mcp.windsor.ai/",
		"ahrefs":          "https://api.ahrefs.com/mcp/mcp",
		"storyblok":       "https://mcp.storyblok.com/mcp",
		"semrush":         "https://mcp.semrush.com/v2/mcp",
		"accuranker":      "https://connect.accuranker.com/mcp",
	}
	wantSupported := map[string]bool{
		"se-ranking": true,
		"sanity":     true,
		"windsor-ai": true,
		"ahrefs":     true,
		"storyblok":  true,
		"semrush":    false,
		"accuranker": false,
	}
	wantSetupNotes := map[string]string{
		"se-ranking": "API access is required. API/MCP trial is available.",
		"sanity":     "Use a Sanity API token with the required project permissions.",
		"windsor-ai": "Use your Windsor.ai API key. Source access depends on your plan.",
		"ahrefs":     "A paid Ahrefs plan and an MCP key are required.",
		"storyblok":  "Use a personal access token. MCP plan access is not yet verified.",
		"semrush":    "Requires Apikey authentication or OAuth. The current bearer-token connector does not support this setup.",
		"accuranker": "Requires OAuth sign-in. The current connector does not support OAuth.",
	}
	for _, item := range decoded.Items {
		expected, ok := want[item.ID]
		if !ok {
			t.Fatalf("unexpected catalog id %q", item.ID)
		}
		if item.Service != expected.Service || item.Title == "" || item.Description == "" {
			t.Fatalf("catalog item %+v is missing service/title/description", item)
		}
		if item.ID == "wordpress" {
			if item.EndpointURL != "" {
				t.Fatalf("wordpress preset must not pin an endpoint, got %q", item.EndpointURL)
			}
			if item.AuthRequired == nil || *item.AuthRequired != true {
				t.Fatalf("wordpress preset must require auth: %+v", item)
			}
			continue
		}
		if item.EndpointURL != wantEndpoints[item.ID] {
			t.Fatalf("preset %q endpoint = %q, want %q", item.ID, item.EndpointURL, wantEndpoints[item.ID])
		}
		if _, isNew := wantSupported[item.ID]; isNew {
			if item.AuthRequired == nil || *item.AuthRequired != true {
				t.Fatalf("preset %q must require auth: %+v", item.ID, item)
			}
			if item.ConnectionSupported == nil || *item.ConnectionSupported != wantSupported[item.ID] {
				t.Fatalf("preset %q connection_supported mismatch: %+v", item.ID, item)
			}
			if item.SetupNote != wantSetupNotes[item.ID] {
				t.Fatalf("preset %q setup_note = %q, want %q", item.ID, item.SetupNote, wantSetupNotes[item.ID])
			}
			continue
		}
		if item.AuthRequired == nil || *item.AuthRequired != false {
			t.Fatalf("preset %q must be marked auth-free: %+v", item.ID, item)
		}
	}
}

func TestMCPCreateCustomWithoutToken(t *testing.T) {
	for _, body := range []string{
		`{"name":"DeepWiki","service":"custom","endpoint_url":"https://mcp.deepwiki.com/mcp"}`,
		`{"name":"DeepWiki","service":"custom","endpoint_url":"https://mcp.deepwiki.com/mcp","bearer_token":""}`,
	} {
		fix := newMCPApprovalFixture(t)
		var dial mcpCapturedDial
		dial.tools = mcpNoAuthTestTools()
		fix.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
			dial.endpoint, dial.token = endpoint, token
			return &fakeMCPSession{tools: dial.tools}, nil
		}
		req := mcpHandlerRequest(t, http.MethodPost, fix.projectID.String(), body, fix.initiatorID)
		rr := httptest.NewRecorder()
		fix.app.handleMCPCreateConnection(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("create body %s status = %d, want 200 (body %q)", body, rr.Code, rr.Body.String())
		}
		if dial.token != "" {
			t.Fatalf("discovery dialed with token %q, want anonymous discovery", dial.token)
		}
		conn := decodeMCPConnection(t, rr)
		if len(conn.Tools) != 2 || conn.Tools[0].Name != "search_docs" {
			t.Fatalf("stored tools = %+v, want the discovered set", conn.Tools)
		}
		if strings.Contains(rr.Body.String(), "enc:") {
			t.Fatal("create response leaks stored credential material")
		}
		var encrypted string
		if err := fix.pool.QueryRow(context.Background(),
			`SELECT encrypted_token FROM project_mcp_connections WHERE id = $1::uuid`, conn.ID).Scan(&encrypted); err != nil {
			t.Fatalf("load stored credential: %v", err)
		}
		if encrypted == "" {
			t.Fatal("no-auth custom connection stored an empty credential, violating the NOT NULL column")
		}
		decrypted, err := fix.app.GSCService.DecryptSecret(encrypted)
		if err != nil {
			t.Fatalf("decrypt stored credential: %v", err)
		}
		if decrypted != "" {
			t.Fatalf("stored credential decrypts to %q, want empty for no-auth", decrypted)
		}
	}
}

func TestMCPCreateWordPressRequiresToken(t *testing.T) {
	fix := newMCPApprovalFixture(t)
	fix.app.MCPConnect = func(context.Context, string, string) (MCPSession, error) {
		t.Error("wordpress without a token must not reach discovery")
		return &fakeMCPSession{}, nil
	}
	for _, body := range []string{
		`{"name":"WP","service":"wordpress","endpoint_url":"https://wp.example/mcp"}`,
		`{"name":"WP","service":"wordpress","endpoint_url":"https://wp.example/mcp","bearer_token":""}`,
	} {
		req := mcpHandlerRequest(t, http.MethodPost, fix.projectID.String(), body, fix.initiatorID)
		rr := httptest.NewRecorder()
		fix.app.handleMCPCreateConnection(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("wordpress body %s status = %d, want 400 (body %q)", body, rr.Code, rr.Body.String())
		}
	}
}

func TestMCPCreateCustomRejectsOverlongToken(t *testing.T) {
	fix := newMCPApprovalFixture(t)
	fix.app.MCPConnect = func(context.Context, string, string) (MCPSession, error) {
		t.Error("overlong token must not reach discovery")
		return &fakeMCPSession{}, nil
	}
	body := `{"name":"DeepWiki","service":"custom","endpoint_url":"https://mcp.deepwiki.com/mcp","bearer_token":"` + strings.Repeat("x", mcpMaxTokenLen+1) + `"}`
	req := mcpHandlerRequest(t, http.MethodPost, fix.projectID.String(), body, fix.initiatorID)
	rr := httptest.NewRecorder()
	fix.app.handleMCPCreateConnection(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong token status = %d, want 400", rr.Code)
	}
}

func mcpCreateWithToken(t *testing.T, fix mcpApprovalFixture, name, token string) mcpConnectionJSON {
	t.Helper()
	fix.app.MCPConnect = func(context.Context, string, string) (MCPSession, error) {
		return &fakeMCPSession{tools: mcpNoAuthTestTools()}, nil
	}
	raw, _ := json.Marshal(map[string]string{"name": name, "service": "custom", "endpoint_url": "https://mcp.deepwiki.com/mcp", "bearer_token": token})
	req := mcpHandlerRequest(t, http.MethodPost, fix.projectID.String(), string(raw), fix.initiatorID)
	rr := httptest.NewRecorder()
	fix.app.handleMCPCreateConnection(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d (body %q)", rr.Code, rr.Body.String())
	}
	return decodeMCPConnection(t, rr)
}

func mcpStoredToken(t *testing.T, fix mcpApprovalFixture, connID string) string {
	t.Helper()
	var encrypted string
	if err := fix.pool.QueryRow(context.Background(),
		`SELECT encrypted_token FROM project_mcp_connections WHERE id = $1::uuid`, connID).Scan(&encrypted); err != nil {
		t.Fatalf("load stored credential: %v", err)
	}
	decrypted, err := fix.app.GSCService.DecryptSecret(encrypted)
	if err != nil {
		t.Fatalf("decrypt stored credential: %v", err)
	}
	return decrypted
}

func TestMCPPatchBlankTokenRetainsCredential(t *testing.T) {
	fix := newMCPApprovalFixture(t)
	const original = "tok-original-secret"
	conn := mcpCreateWithToken(t, fix, "DeepWiki", original)
	before, err := func() (string, error) {
		var encrypted string
		err := fix.pool.QueryRow(context.Background(),
			`SELECT encrypted_token FROM project_mcp_connections WHERE id = $1::uuid`, conn.ID).Scan(&encrypted)
		return encrypted, err
	}()
	if err != nil {
		t.Fatalf("load stored credential: %v", err)
	}
	fix.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		if token != original {
			t.Errorf("patch rediscovery dialed with %q, want the retained credential", token)
		}
		return &fakeMCPSession{tools: mcpNoAuthTestTools()}, nil
	}
	for _, body := range []string{`{"bearer_token":""}`, `{"endpoint_url":"https://mcp.deepwiki.com/mcp"}`} {
		req := mcpConnectionRequest(t, http.MethodPatch, fix.projectID.String(), conn.ID, body, fix.initiatorID)
		rr := httptest.NewRecorder()
		fix.app.handleMCPPatchConnection(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch body %s status = %d (body %q)", body, rr.Code, rr.Body.String())
		}
	}
	var after string
	if err := fix.pool.QueryRow(context.Background(),
		`SELECT encrypted_token FROM project_mcp_connections WHERE id = $1::uuid`, conn.ID).Scan(&after); err != nil {
		t.Fatalf("reload stored credential: %v", err)
	}
	if after != before {
		t.Fatal("a blank token field silently replaced the stored credential")
	}
	if got := mcpStoredToken(t, fix, conn.ID); got != original {
		t.Fatalf("stored credential decrypts to %q, want the retained token", got)
	}
	// An explicitly supplied token still replaces the stored one.
	raw, _ := json.Marshal(map[string]string{"bearer_token": "tok-rotated"})
	req := mcpConnectionRequest(t, http.MethodPatch, fix.projectID.String(), conn.ID, string(raw), fix.initiatorID)
	rr := httptest.NewRecorder()
	fix.app.MCPConnect = func(context.Context, string, string) (MCPSession, error) {
		return &fakeMCPSession{tools: mcpNoAuthTestTools()}, nil
	}
	fix.app.handleMCPPatchConnection(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate status = %d (body %q)", rr.Code, rr.Body.String())
	}
	if got := mcpStoredToken(t, fix, conn.ID); got != "tok-rotated" {
		t.Fatalf("stored credential decrypts to %q, want the rotated token", got)
	}
}

func TestMCPCheckNoAuthCustom(t *testing.T) {
	fix := newMCPApprovalFixture(t)
	fix.app.MCPConnect = func(context.Context, string, string) (MCPSession, error) {
		return &fakeMCPSession{tools: mcpNoAuthTestTools()}, nil
	}
	created := mcpCreateWithToken(t, fix, "DeepWiki", "")
	_ = created
	// Recreate through the no-auth path to keep the test honest: an empty
	// supplied token must behave like a missing one.
	var dialed string
	fix.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		dialed = token
		return &fakeMCPSession{tools: mcpNoAuthTestTools()}, nil
	}
	var connID string
	if err := fix.pool.QueryRow(context.Background(),
		`SELECT id::text FROM project_mcp_connections WHERE project_id = $1::uuid AND name = 'DeepWiki'`, fix.projectID.String()).Scan(&connID); err != nil {
		t.Fatalf("find connection: %v", err)
	}
	req := mcpConnectionRequest(t, http.MethodPost, fix.projectID.String(), connID, `{}`, fix.initiatorID)
	rr := httptest.NewRecorder()
	fix.app.handleMCPCheckConnection(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("check status = %d (body %q)", rr.Code, rr.Body.String())
	}
	if dialed != "" {
		t.Fatalf("check dialed with %q, want anonymous rediscovery", dialed)
	}
}

func TestMCPCheckWordPressEmptyCredentialFailsClosed(t *testing.T) {
	fix := newMCPApprovalFixture(t)
	emptyEnc, err := fix.app.GSCService.EncryptSecret("")
	if err != nil || emptyEnc == "" {
		t.Fatalf("encrypt empty = %q, %v; need opaque stored credentials", emptyEnc, err)
	}
	var connID string
	if err := fix.pool.QueryRow(context.Background(),
		`INSERT INTO project_mcp_connections(project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at)
		 VALUES($1::uuid, 'WP', 'wordpress', 'https://wp.example/mcp', $2, gen_random_uuid(), '[]', now()) RETURNING id::text`,
		fix.projectID.String(), emptyEnc).Scan(&connID); err != nil {
		t.Fatalf("insert wordpress connection: %v", err)
	}
	fix.app.MCPConnect = func(context.Context, string, string) (MCPSession, error) {
		t.Error("wordpress with an empty credential must fail closed before discovery")
		return &fakeMCPSession{}, nil
	}
	req := mcpConnectionRequest(t, http.MethodPost, fix.projectID.String(), connID, `{}`, fix.initiatorID)
	rr := httptest.NewRecorder()
	fix.app.handleMCPCheckConnection(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatal("wordpress check with an empty credential succeeded; must fail closed")
	}
}
