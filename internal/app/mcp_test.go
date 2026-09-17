package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/config"
)

func TestMCPDisabledOmitsRoutes(t *testing.T) {
	router := (&App{}).Router()

	for _, path := range []string{
		"/mcp",
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 when MCP is disabled", path, rec.Code)
		}
	}
}

func TestMCPProtectedResourceMetadata(t *testing.T) {
	router := mcpTestRouter()
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		var body struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			BearerMethods        []string `json:"bearer_methods_supported"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if body.Resource != "http://localhost:8080/mcp" {
			t.Fatalf("%s resource = %q", path, body.Resource)
		}
		if len(body.AuthorizationServers) != 1 || body.AuthorizationServers[0] != "https://example.supabase.co/auth/v1" {
			t.Fatalf("%s authorization_servers = %#v", path, body.AuthorizationServers)
		}
		if len(body.BearerMethods) != 1 || body.BearerMethods[0] != "header" {
			t.Fatalf("%s bearer_methods_supported = %#v", path, body.BearerMethods)
		}
	}
}

func TestMCPUnauthenticatedChallenge(t *testing.T) {
	router := mcpTestRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
	got := rec.Header().Get("WWW-Authenticate")
	want := `resource_metadata="http://localhost:8080/.well-known/oauth-protected-resource/mcp"`
	if !strings.Contains(got, want) {
		t.Fatalf("WWW-Authenticate = %q, want it to contain %q", got, want)
	}
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(strings.ToLower(string(body)), "rvs_live_") {
		t.Fatalf("401 body leaked a credential: %s", body)
	}
}

func TestMCPAudienceOK(t *testing.T) {
	resource := "http://localhost:8080/mcp"
	legacy := "authenticated"
	tests := []struct {
		name   string
		claims []string
		want   bool
	}{
		{name: "empty", want: true},
		{name: "legacy dashboard", claims: []string{"authenticated"}, want: true},
		{name: "resource", claims: []string{resource}, want: true},
		{name: "foreign", claims: []string{"https://evil.example/mcp"}, want: false},
		{name: "mixed foreign", claims: []string{"authenticated", "https://evil.example/mcp"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpAudienceOK(tc.claims, resource, legacy); got != tc.want {
				t.Fatalf("mcpAudienceOK(%q) = %v, want %v", tc.claims, got, tc.want)
			}
		})
	}
}

func mcpTestRouter() http.Handler {
	return (&App{Config: config.Config{
		MCPResourceURL:      "http://localhost:8080/mcp",
		SupabaseJWTIssuer:   "https://example.supabase.co/auth/v1",
		SupabaseJWTAudience: "authenticated",
	}}).Router()
}
