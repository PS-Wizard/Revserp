package crawler

import (
	"net/url"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	baseURL, err := url.Parse("https://revketer.ai/services/")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	tests := []struct {
		name        string
		candidate   string
		baseURL     *url.URL
		wantURL     string
		wantError   bool
	}{
		{
			name:      "absolute url lowercases host and strips fragment",
			candidate: "HTTPS://RevKeter.AI/About#team",
			wantURL:   "https://revketer.ai/About",
		},
		{
			name:      "relative url resolves against base",
			candidate: "../contact",
			baseURL:   baseURL,
			wantURL:   "https://revketer.ai/contact",
		},
		{
			name:      "empty path becomes slash",
			candidate: "https://revketer.ai",
			wantURL:   "https://revketer.ai/",
		},
		{
			name:      "non http scheme is rejected",
			candidate: "mailto:hello@revketer.ai",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalizedURL, err := NormalizeURL(test.candidate, test.baseURL)
			if test.wantError {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}

				return
			}

			if err != nil {
				t.Fatalf("normalize url: %v", err)
			}

			if normalizedURL.String() != test.wantURL {
				t.Fatalf("got %q, want %q", normalizedURL.String(), test.wantURL)
			}
		})
	}
}

func TestIsInternalURL(t *testing.T) {
	rootURL, err := url.Parse("https://revketer.ai/")
	if err != nil {
		t.Fatalf("parse root url: %v", err)
	}

	internalURL, err := url.Parse("https://revketer.ai/about")
	if err != nil {
		t.Fatalf("parse internal url: %v", err)
	}

	externalURL, err := url.Parse("https://vercel.com/")
	if err != nil {
		t.Fatalf("parse external url: %v", err)
	}

	if !IsInternalURL(rootURL, internalURL) {
		t.Fatalf("expected internal url to be internal")
	}

	if IsInternalURL(rootURL, externalURL) {
		t.Fatalf("expected external url to be external")
	}
}
