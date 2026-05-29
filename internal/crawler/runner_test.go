package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"
)

func TestRunnerRunCrawlsInternalPagesUpToMaxDepth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html>
								<html><head><title>home</title></head><body>
									<a href="/about">About</a>
									<a href="https://vercel.com">External</a>
								</body></html>`)
		case "/about":
			fmt.Fprint(writer, `<!DOCTYPE html>
								<html><head><title>about</title></head><body>
									<a href="/team">Team</a>
								</body></html>`)
		case "/team":
			fmt.Fprint(writer, `<!DOCTYPE html>
								<html><head><title>team</title></head><body></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "")
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    10,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	var crawledPaths []string
	for _, result := range results {
		if result.ProcessErr != nil {
			t.Fatalf("unexpected process error: %v", result.ProcessErr)
		}

		crawledPaths = append(crawledPaths, mustParseURL(t, result.Fetch.FinalURL).Path)
	}

	sort.Strings(crawledPaths)

	if crawledPaths[0] != "/" || crawledPaths[1] != "/about" {
		t.Fatalf("got crawled paths %#v", crawledPaths)
	}
}

func TestRunnerRunRespectsMaxPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html>
							<html><head><title>page</title></head><body>
								<a href="/one">One</a>
								<a href="/two">Two</a>
								<a href="/three">Three</a>
							</body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "")
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    2,
		MaxPages:    2,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url %q: %v", rawURL, err)
	}

	return parsedURL
}
