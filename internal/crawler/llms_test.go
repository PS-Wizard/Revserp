package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRunnerDetectsLlmsTxtPresent(t *testing.T) {
	allowLoopbackDialsForTest(t)

	llmsRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			// Link to /llms.txt from the homepage so a normal crawl would schedule
			// it; only the reserved probe may fetch it.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/llms.txt">llms</a></body></html>`)
		case "/llms.txt":
			llmsRequests++
			w.WriteHeader(200)
			fmt.Fprint(w, "# LLMs\nsome content")
		case "/robots.txt", "/sitemap.xml":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	store := &testResultStore{}
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    5,
	}, 2, fetcher, parser).WithStore(store)

	_, summary, err := runner.RunAndPersistWithSummary(context.Background(), pgtype.UUID{}, server.URL)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.HasLlmsTxt == nil {
		t.Fatalf("expected has_llms_txt to be non-nil, got nil")
	}
	if !*summary.HasLlmsTxt {
		t.Fatalf("expected has_llms_txt true, got false")
	}
	if !store.completedHasLlmsTxt.Valid || !store.completedHasLlmsTxt.Bool {
		t.Fatalf("expected persisted has_llms_txt true valid, got %+v", store.completedHasLlmsTxt)
	}
	if llmsRequests != 1 {
		t.Fatalf("expected exactly one /llms.txt request (the probe), got %d", llmsRequests)
	}
}

func TestRunnerDetectsLlmsTxtMissing(t *testing.T) {
	allowLoopbackDialsForTest(t)

	cases := []struct {
		name       string
		status     int
		body       string
		expectTrue bool
	}{
		{"404 missing", 404, "not found", false},
		{"200 empty", 200, "", false},
		{"200 whitespace", 200, "   \n", false},
		{"500 error", 500, "err", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowLoopbackDialsForTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprint(w, `<!DOCTYPE html><html><head><title>home</title></head><body></body></html>`)
				case "/llms.txt":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				case "/robots.txt", "/sitemap.xml":
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
			parser := NewParser()
			store := &testResultStore{}
			runner := NewRunner(CrawlerConfig{
				AllowedHost: mustParseURL(t, server.URL).Host,
				MaxDepth:    1,
				MaxPages:    5,
			}, 2, fetcher, parser).WithStore(store)

			_, summary, err := runner.RunAndPersistWithSummary(context.Background(), pgtype.UUID{}, server.URL)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if summary.HasLlmsTxt == nil {
				t.Fatalf("expected has_llms_txt non-nil")
			}
			if *summary.HasLlmsTxt != tc.expectTrue {
				t.Fatalf("expected has_llms_txt %v got %v", tc.expectTrue, *summary.HasLlmsTxt)
			}
			if !store.completedHasLlmsTxt.Valid || store.completedHasLlmsTxt.Bool != tc.expectTrue {
				t.Fatalf("expected persisted has_llms_txt valid %v, got %+v", tc.expectTrue, store.completedHasLlmsTxt)
			}
		})
	}
}
