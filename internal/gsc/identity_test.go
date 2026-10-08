package gsc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func stubUserinfoServer(responseStatus int, responseBody string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(responseStatus)
		_, _ = w.Write([]byte(responseBody))
	}))
}

func TestFetchVerifiedGoogleIdentity(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantSubject  string
		wantEmail    string
		wantErrMatch string
	}{
		{
			name:        "verified bool email",
			status:      http.StatusOK,
			body:        `{"sub":"107test","email":"owner@example.com","email_verified":true}`,
			wantSubject: "107test",
			wantEmail:   "owner@example.com",
		},
		{
			name:        "verified string email",
			status:      http.StatusOK,
			body:        `{"sub":"107test","email":"owner@example.com","email_verified":"true"}`,
			wantSubject: "107test",
			wantEmail:   "owner@example.com",
		},
		{
			name:        "absent email_verified",
			status:      http.StatusOK,
			body:        `{"sub":"107test","email":"owner@example.com"}`,
			wantSubject: "107test",
			wantEmail:   "owner@example.com",
		},
		{
			name:         "unverified email fails closed",
			status:       http.StatusOK,
			body:         `{"sub":"107test","email":"owner@example.com","email_verified":false}`,
			wantErrMatch: "not verified",
		},
		{
			name:         "missing subject fails",
			status:       http.StatusOK,
			body:         `{"email":"owner@example.com"}`,
			wantErrMatch: "subject",
		},
		{
			name:         "missing email fails",
			status:       http.StatusOK,
			body:         `{"sub":"107test"}`,
			wantErrMatch: "email",
		},
		{
			name:         "non-2xx fails",
			status:       http.StatusUnauthorized,
			body:         `{"error":"invalid_token"}`,
			wantErrMatch: "verify Google account identity",
		},
		{
			name:         "invalid json fails",
			status:       http.StatusOK,
			body:         `not json`,
			wantErrMatch: "invalid identity response",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := stubUserinfoServer(test.status, test.body)
			defer server.Close()
			service := NewService("client", "secret", "https://app.example/callback", "encryption-secret", 0)
			service.userinfoBaseURL = server.URL
			service.httpClient = server.Client()

			identity, err := service.FetchVerifiedGoogleIdentity(context.Background(), "access-token")
			if test.wantErrMatch != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", test.wantErrMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if identity.Subject != test.wantSubject || identity.Email != test.wantEmail {
				t.Fatalf("identity = %+v, want subject %q email %q", identity, test.wantSubject, test.wantEmail)
			}
		})
	}
}

func TestFetchVerifiedGoogleIdentityRequiresToken(t *testing.T) {
	service := NewService("client", "secret", "https://app.example/callback", "encryption-secret", 0)
	if _, err := service.FetchVerifiedGoogleIdentity(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty access token, got nil")
	}
}
