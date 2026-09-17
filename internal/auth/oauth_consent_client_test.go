package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The OAuth consent endpoints are Supabase Auth's, so these tests pin the exact
// paths, headers, and body we send. Supabase accepts a request with no Origin
// header, which is why none is asserted here.

const (
	consentTestAuthorizationID = "11111111-2222-3333-4444-555555555555"
	consentTestAccessToken     = "user-access-token"
)

func TestGetOAuthAuthorizationSendsUserToken(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	var gotAPIKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("apikey")
		if r.Header.Get("Origin") != "" {
			t.Errorf("Origin header sent: %q", r.Header.Get("Origin"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_id": consentTestAuthorizationID,
			"redirect_uri":     "https://claude.ai/api/mcp/auth_callback",
			"scope":            "email profile",
			"client": map[string]string{
				"id":       "9a8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d",
				"name":     "Claude",
				"logo_uri": "https://claude.ai/logo.png",
			},
			"user": map[string]string{"id": "user-id", "email": "user@example.com"},
		})
	}))
	defer server.Close()

	client := NewSupabaseClient(server.URL, "anon-key")
	authorization, err := client.GetOAuthAuthorization(context.Background(), consentTestAccessToken, consentTestAuthorizationID)
	if err != nil {
		t.Fatalf("GetOAuthAuthorization: %v", err)
	}

	if want := "/oauth/authorizations/" + consentTestAuthorizationID; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := "Bearer " + consentTestAccessToken; gotAuthorization != want {
		t.Errorf("Authorization = %q, want %q", gotAuthorization, want)
	}
	if gotAPIKey != "anon-key" {
		t.Errorf("apikey = %q, want %q", gotAPIKey, "anon-key")
	}
	if authorization.Client.Name != "Claude" {
		t.Errorf("client name = %q, want %q", authorization.Client.Name, "Claude")
	}
	if authorization.RedirectURI != "https://claude.ai/api/mcp/auth_callback" {
		t.Errorf("redirect uri = %q", authorization.RedirectURI)
	}
	if authorization.Scope != "email profile" {
		t.Errorf("scope = %q, want %q", authorization.Scope, "email profile")
	}
	if authorization.RedirectURL != "" {
		t.Errorf("redirect url = %q, want empty for a pending request", authorization.RedirectURL)
	}
}

func TestGetOAuthAuthorizationDecodesAutoApproval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Supabase answers with only a redirect_url once the user has already
		// consented to the requested scopes.
		_ = json.NewEncoder(w).Encode(map[string]string{"redirect_url": "https://claude.ai/cb?code=abc&state=xyz"})
	}))
	defer server.Close()

	client := NewSupabaseClient(server.URL, "anon-key")
	authorization, err := client.GetOAuthAuthorization(context.Background(), consentTestAccessToken, consentTestAuthorizationID)
	if err != nil {
		t.Fatalf("GetOAuthAuthorization: %v", err)
	}
	if authorization.RedirectURL != "https://claude.ai/cb?code=abc&state=xyz" {
		t.Errorf("redirect url = %q", authorization.RedirectURL)
	}
	if authorization.AuthorizationID != "" {
		t.Errorf("authorization id = %q, want empty on auto-approval", authorization.AuthorizationID)
	}
}

func TestSubmitOAuthConsentPostsAction(t *testing.T) {
	for _, action := range []string{OAuthConsentApprove, OAuthConsentDeny} {
		t.Run(action, func(t *testing.T) {
			var gotMethod string
			var gotPath string
			var gotBody map[string]string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode consent body: %v", err)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"redirect_url": "https://claude.ai/cb?code=abc"})
			}))
			defer server.Close()

			client := NewSupabaseClient(server.URL, "anon-key")
			consent, err := client.SubmitOAuthConsent(context.Background(), consentTestAccessToken, consentTestAuthorizationID, action)
			if err != nil {
				t.Fatalf("SubmitOAuthConsent: %v", err)
			}

			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
			}
			if want := "/oauth/authorizations/" + consentTestAuthorizationID + "/consent"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			if gotBody["action"] != action {
				t.Errorf("action = %q, want %q", gotBody["action"], action)
			}
			if consent.RedirectURL != "https://claude.ai/cb?code=abc" {
				t.Errorf("redirect url = %q", consent.RedirectURL)
			}
		})
	}
}

func TestGetOAuthAuthorizationMapsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_not_found", "msg": "authorization not found"})
	}))
	defer server.Close()

	client := NewSupabaseClient(server.URL, "anon-key")
	_, err := client.GetOAuthAuthorization(context.Background(), consentTestAccessToken, consentTestAuthorizationID)

	var authError *SupabaseAuthError
	if !errors.As(err, &authError) {
		t.Fatalf("error = %v, want SupabaseAuthError", err)
	}
	if authError.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", authError.StatusCode, http.StatusNotFound)
	}
}

func TestOAuthAuthorizationRequiresAccessToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	client := NewSupabaseClient(server.URL, "anon-key")
	if _, err := client.GetOAuthAuthorization(context.Background(), "  ", consentTestAuthorizationID); err == nil {
		t.Error("GetOAuthAuthorization with a blank token returned no error")
	}
	if _, err := client.SubmitOAuthConsent(context.Background(), "", consentTestAuthorizationID, OAuthConsentApprove); err == nil {
		t.Error("SubmitOAuthConsent with a blank token returned no error")
	}
	if requests != 0 {
		t.Errorf("upstream requests = %d, want 0", requests)
	}
}
