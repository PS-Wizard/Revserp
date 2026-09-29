package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
)

// A removed route returns 404 rather than an authentication error.
func TestLegacyAPIKeyRoutesAreAbsent(t *testing.T) {
	router := (&App{}).Router()
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api-keys", ""},
		{http.MethodPost, "/api-keys/00000000-0000-0000-0000-000000000001/revoke", ""},
		{http.MethodPost, "/agent/setup-codes", ""},
		{http.MethodPost, "/agent/setup", `{"code":"rvs_setup_test"}`},
		{http.MethodGet, "/v1/me", ""},
		{http.MethodGet, "/v1/projects/00000000-0000-0000-0000-000000000001", ""},
		{http.MethodGet, "/v1/crawls/00000000-0000-0000-0000-000000000001", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404, body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// The MCP endpoint must reject the retired API-key format.
func TestMCPRejectsLegacyAPIKey(t *testing.T) {
	router := (&App{Config: config.Config{
		MCPResourceURL:      "http://localhost:8080/mcp",
		SupabaseJWTIssuer:   "https://example.supabase.co/auth/v1",
		SupabaseJWTAudience: "authenticated",
	}}).Router()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer rvs_live_definitelynotarealkeyvalue0000000000")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /mcp with rvs_live_ status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestOAuthConsentRoutesStillRequireSession(t *testing.T) {
	app := &App{SessionManager: internalauth.NewSessionManager(nil, nil, nil, "revserp_session", "", time.Hour, false)}
	router := app.Router()
	id := "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/oauth/authorizations/" + id, ""},
		{http.MethodPost, "/oauth/authorizations/" + id + "/consent", `{"action":"approve"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("%s %s status = 404, want route to remain mounted", tc.method, tc.path)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 without session, body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}
