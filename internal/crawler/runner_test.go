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

	"github.com/jackc/pgx/v5/pgtype"
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

func TestRunnerRunAndPersistCallsStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/about">About</a></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "")
	parser := NewParser()
	store := &testResultStore{}
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    2,
	}, 2, fetcher, parser).WithStore(store)

	results, err := runner.RunAndPersist(context.Background(), pgtype.UUID{}, server.URL)
	if err != nil {
		t.Fatalf("run and persist crawler: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	if store.persistedCount != 2 {
		t.Fatalf("got persisted count %d, want 2", store.persistedCount)
	}
}

func TestRunnerRunAndPersistFailsOnStoreError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "")
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    0,
		MaxPages:    1,
	}, 1, fetcher, parser).WithStore(&testResultStore{persistErr: fmt.Errorf("boom")})

	results, err := runner.RunAndPersist(context.Background(), pgtype.UUID{}, server.URL)
	if err == nil {
		t.Fatalf("expected persist error but got nil")
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

type testResultStore struct {
	persistedCount int
	persistErr     error
}

func (store *testResultStore) PersistResult(_ context.Context, _ pgtype.UUID, _ string, _ CrawlResult) error {
	if store.persistErr != nil {
		return store.persistErr
	}

	store.persistedCount++
	return nil
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url %q: %v", rawURL, err)
	}

	return parsedURL
}
