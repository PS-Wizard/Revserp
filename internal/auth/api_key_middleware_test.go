package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAPIKeyRejectsMissingMalformedAndMultipleHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		cookie  bool
	}{
		{name: "missing"},
		{name: "cookie does not authenticate", cookie: true},
		{name: "malformed", headers: []string{"Basic abc"}},
		{name: "multiple values in one header", headers: []string{"Bearer first,Bearer second"}},
		{name: "multiple header fields", headers: []string{"Bearer first", "Bearer second"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := RequireAPIKey(NewAPIKeyManager(nil))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			request := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			for _, header := range tc.headers {
				request.Header.Add("Authorization", header)
			}
			if tc.cookie {
				request.AddCookie(&http.Cookie{Name: "revserp_session", Value: "browser-session"})
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			if called {
				t.Fatal("next handler was called")
			}
		})
	}
}
