package crawler

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNeedsJSRenderLiveURLs(t *testing.T) {
	if os.Getenv("RUN_LIVE_RENDER_DETECTOR_TESTS") != "1" {
		t.Skip("set RUN_LIVE_RENDER_DETECTOR_TESTS=1 to run live detector tests")
	}

	fetcher := NewFetcher(15*time.Second, "revserp-live-test/0.1", 0, time.Second, 15*time.Second)
	parser := NewParser()

	tests := []struct {
		name            string
		url             string
		wantNeedsRender bool
	}{
		{
			name:            "google search results page",
			url:             "https://www.google.com/search?q=hi",
			wantNeedsRender: true,
		},
		{
			name:            "amazon home page",
			url:             "https://www.amazon.com/",
			wantNeedsRender: false,
		},
		{
			name:            "example home page",
			url:             "https://example.com",
			wantNeedsRender: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetchResult := fetcher.Fetch(context.Background(), test.url)
			if fetchResult.FetchError != nil {
				t.Fatalf("fetch url %q: %v", test.url, fetchResult.FetchError)
			}

			parsedPage, err := parser.ParseHTML(fetchResult.FinalURL, fetchResult.ContentType, fetchResult.Body)
			if err != nil {
				t.Fatalf("parse html for %q: %v", test.url, err)
			}

			decision := NeedsJSRender(fetchResult, &parsedPage)
			t.Logf("url=%q final_url=%q needs_render=%v reasons=%#v title=%q visible_text_len=%d link_count=%d", test.url, fetchResult.FinalURL, decision.NeedsRender, decision.Reasons, parsedPage.Title, len(parsedPage.VisibleText), len(parsedPage.Links))
			if decision.NeedsRender != test.wantNeedsRender {
				t.Fatalf("got needs render %v for %q with reasons %#v, want %v", decision.NeedsRender, test.url, decision.Reasons, test.wantNeedsRender)
			}
		})
	}
}
