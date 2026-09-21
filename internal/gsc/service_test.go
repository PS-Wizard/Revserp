package gsc

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildAuthURLRequestsSearchAndAnalyticsReadOnlyScopes(t *testing.T) {
	service := NewService("client", "secret", "https://app.example/auth/google/callback", "encryption-secret", 0)
	authURL, err := service.BuildAuthURL("state")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}

	scopes := make(map[string]bool)
	for _, scope := range strings.Fields(parsed.Query().Get("scope")) {
		scopes[scope] = true
	}
	for _, scope := range []string{googleWebmastersReadOnlyScope, googleAnalyticsReadOnlyScope} {
		if !scopes[scope] {
			t.Errorf("OAuth scope %q is missing", scope)
		}
	}
}
