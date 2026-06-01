package crawler

import (
	"strings"
	"testing"
)

func TestNeedsJSRender(t *testing.T) {
	tests := []struct {
		name            string
		fetchResult     FetchResult
		parsedPage      *ParsedPage
		wantNeedsRender bool
		wantReasons     []string
	}{
		{
			name: "inline script data with sparse visible text triggers render",
			fetchResult: FetchResult{
				ContentType: "text/html; charset=utf-8",
				Body:        []byte(`<!DOCTYPE html><html><head><title>Quotes to Scrape</title></head><body><h1>Quotes to Scrape</h1><a href="/login">Login</a><script>var data = [` + strings.Repeat(`{"text":"A quote body with useful data"},`, 80) + `];</script></body></html>`),
			},
			parsedPage: &ParsedPage{
				Title:       "Quotes to Scrape",
				H1:          "Quotes to Scrape",
				VisibleText: "Quotes to Scrape Login",
				Links:       []ParsedLink{{TargetURL: "https://example.com/login"}},
			},
			wantNeedsRender: true,
			wantReasons: []string{
				"html contains substantial inline script data",
			},
		},
		{
			name: "enablejs url triggers render even with rich html",
			fetchResult: FetchResult{
				FinalURL:    "https://www.google.com/httpservice/retry/enablejs?sei=test",
				ContentType: "text/html; charset=utf-8",
				Body:        []byte(`<!DOCTYPE html><html><head><title>Localized JavaScript Page</title></head><body><p>` + strings.Repeat("content ", 100) + `</p><a href="/one">One</a><a href="/two">Two</a><a href="/three">Three</a><a href="/four">Four</a><a href="/five">Five</a></body></html>`),
			},
			parsedPage: &ParsedPage{
				Title:       "Localized JavaScript Page",
				VisibleText: strings.Repeat("content ", 100),
				Links:       []ParsedLink{{TargetURL: "https://example.com/one"}, {TargetURL: "https://example.com/two"}, {TargetURL: "https://example.com/three"}, {TargetURL: "https://example.com/four"}, {TargetURL: "https://example.com/five"}},
			},
			wantNeedsRender: true,
			wantReasons: []string{
				"html says javascript is required",
			},
		},
		{
			name: "non html does not trigger render",
			fetchResult: FetchResult{
				ContentType: "application/pdf",
				Body:        []byte("not html"),
			},
			wantNeedsRender: false,
		},
		{
			name: "spa shell markers trigger render",
			fetchResult: FetchResult{
				ContentType: "text/html; charset=utf-8",
				Body:        []byte(`<!DOCTYPE html><html><head></head><body><div id="__next"></div></body></html>`),
			},
			parsedPage: &ParsedPage{
				VisibleText: "",
				Links:       nil,
				Title:       "",
			},
			wantNeedsRender: true,
			wantReasons: []string{
				"html contains javascript app shell markers",
			},
		},
		{
			name: "javascript required message triggers render",
			fetchResult: FetchResult{
				ContentType: "text/html; charset=utf-8",
				Body:        []byte(`<!DOCTYPE html><html><body><noscript>Please enable JavaScript to continue.</noscript></body></html>`),
			},
			parsedPage: &ParsedPage{
				VisibleText: "Please enable JavaScript to continue.",
				Links:       nil,
				Title:       "",
			},
			wantNeedsRender: true,
			wantReasons: []string{
				"html says javascript is required",
			},
		},
		{
			name: "large body with sparse extracted content triggers render",
			fetchResult: FetchResult{
				ContentType: "text/html; charset=utf-8",
				Body:        []byte("<html><body>" + strings.Repeat(" ", 12_000) + "</body></html>"),
			},
			parsedPage: &ParsedPage{
				VisibleText: "short",
				Links:       []ParsedLink{{TargetURL: "https://example.com/one"}},
				Title:       "Tiny",
			},
			wantNeedsRender: true,
			wantReasons: []string{
				"html body is large but extracted content is sparse",
			},
		},
		{
			name: "content rich html does not trigger render",
			fetchResult: FetchResult{
				ContentType: "text/html; charset=utf-8",
				Body:        []byte("<html><body>plain content</body></html>"),
			},
			parsedPage: &ParsedPage{
				Title:       "Useful Page",
				VisibleText: strings.Repeat("content ", 40),
				Links: []ParsedLink{
					{TargetURL: "https://example.com/one"},
					{TargetURL: "https://example.com/two"},
					{TargetURL: "https://example.com/three"},
					{TargetURL: "https://example.com/four"},
					{TargetURL: "https://example.com/five"},
				},
			},
			wantNeedsRender: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := NeedsJSRender(test.fetchResult, test.parsedPage)
			if decision.NeedsRender != test.wantNeedsRender {
				t.Fatalf("got needs render %v, want %v", decision.NeedsRender, test.wantNeedsRender)
			}

			for _, wantReason := range test.wantReasons {
				if !containsString(decision.Reasons, wantReason) {
					t.Fatalf("missing reason %q in %#v", wantReason, decision.Reasons)
				}
			}
		})
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}
