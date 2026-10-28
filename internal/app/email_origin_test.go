package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/ps-wizard/revserp/internal/config"
)

func TestResolveEmailOrigin(t *testing.T) {
	cfg := config.Config{
		FrontendURL:        "https://revserp.ai",
		CORSAllowedOrigins: []string{"http://localhost:3000", "https://beta.revserp.ai", "https://revserp.ai"},
	}
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"allowed beta origin wins", "https://beta.revserp.ai", "https://beta.revserp.ai"},
		{"allowed localhost wins", "http://localhost:3000", "http://localhost:3000"},
		{"unknown origin falls back", "https://evil.example.com", "https://revserp.ai"},
		{"missing origin falls back", "", "https://revserp.ai"},
		{"trailing slash tolerated", "https://beta.revserp.ai/", "https://beta.revserp.ai"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "/", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := ResolveEmailOrigin(req, cfg); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDomainAcceptsMail(t *testing.T) {
	if testing.Short() {
		t.Skip("needs DNS")
	}
	if !domainAcceptsMail(context.Background(), "someone@gmail.com") {
		t.Fatal("gmail.com should accept mail")
	}
	if domainAcceptsMail(context.Background(), "x@randoafsdkljfasd.com") {
		t.Fatal("made-up domain should not accept mail")
	}
}
