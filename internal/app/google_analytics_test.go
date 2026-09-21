package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/ga"
)

func TestHasGoogleScopeMatchesWholeScope(t *testing.T) {
	tests := []struct {
		scope string
		want  bool
	}{
		{"https://www.googleapis.com/auth/analytics.readonly", true},
		{"https://www.googleapis.com/auth/webmasters.readonly https://www.googleapis.com/auth/analytics.readonly", true},
		{"https://www.googleapis.com/auth/analytics.readonly.extra", false},
	}
	for _, test := range tests {
		if got := hasGoogleScope(test.scope, googleAnalyticsReadOnlyScope); got != test.want {
			t.Errorf("hasGoogleScope(%q) = %v, want %v", test.scope, got, test.want)
		}
	}
}

func TestRankAnalyticsPropertiesPrefersProjectMatches(t *testing.T) {
	properties := rankAnalyticsProperties("https://example.com", "Example", []ga.Property{
		{PropertyID: "2", DisplayName: "Other", AccountDisplayName: "Z account"},
		{PropertyID: "1", DisplayName: "Example site", AccountDisplayName: "A account"},
	})
	if properties[0].PropertyID != "1" {
		t.Fatalf("first property = %#v, want project match", properties[0])
	}
}

func TestWriteGoogleOAuthRedirectPreservesReturnFragment(t *testing.T) {
	app := &App{Config: config.Config{FrontendURL: "https://app.example/base"}}
	response := httptest.NewRecorder()
	app.writeGoogleOAuthRedirect(response, httptest.NewRequest(http.MethodGet, "/auth/google/callback", nil), sqlc.GoogleOauthState{ReturnPath: "/projects/1?tab=reports#analytics"}, "")
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Fragment != "analytics" {
		t.Fatalf("fragment = %q, want analytics", location.Fragment)
	}
}
