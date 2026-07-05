package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubRenderer struct {
	fetchResult FetchResult
	err         error
	callCount   int
}

func (renderer *stubRenderer) RenderHTML(_ context.Context, _ string) (FetchResult, error) {
	renderer.callCount++
	if renderer.err != nil {
		return FetchResult{}, renderer.err
	}
	return renderer.fetchResult, nil
}

func TestProcessJobUsesRenderedHTMLWhenItImprovesExtraction(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title></title></head><body><div id="app"></div><script src="/app.js"></script><script src="/vendor.js"></script><script src="/runtime.js"></script><script src="/chunk.js"></script><script src="/extra.js"></script></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	renderer := &stubRenderer{
		fetchResult: FetchResult{
			FinalURL:    server.URL,
			StatusCode:  http.StatusOK,
			ContentType: "text/html; charset=utf-8",
			Body:        []byte(`<!DOCTYPE html><html><head><title>Rendered Title</title></head><body><main><h1>Rendered with JavaScript</h1><p>This is enough rendered content to clearly beat the shell page used by the raw HTTP fetch path for this test fixture.</p><a href="/about">About</a><a href="/pricing">Pricing</a><a href="/contact">Contact</a><a href="/blog">Blog</a></main></body></html>`),
		},
	}

	result := ProcessJob(context.Background(), fetcher, parser, renderer, CrawlJob{URL: server.URL, Depth: 0})
	if result.ProcessErr != nil {
		t.Fatalf("process job: %v", result.ProcessErr)
	}
	if renderer.callCount != 1 {
		t.Fatalf("got renderer calls %d, want 1", renderer.callCount)
	}
	if result.ParsedPage == nil {
		t.Fatalf("expected parsed page but got nil")
	}
	if result.ParsedPage.Title != "Rendered Title" {
		t.Fatalf("got title %q", result.ParsedPage.Title)
	}
	if result.Fetch.Body == nil || string(result.Fetch.Body) == "" {
		t.Fatalf("expected rendered body to be used")
	}
}

func TestProcessJobKeepsRawHTMLWhenRendererFails(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title></title></head><body><div id="app"></div><script src="/app.js"></script><script src="/vendor.js"></script><script src="/runtime.js"></script><script src="/chunk.js"></script><script src="/extra.js"></script></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	renderer := &stubRenderer{err: fmt.Errorf("boom")}

	result := ProcessJob(context.Background(), fetcher, parser, renderer, CrawlJob{URL: server.URL, Depth: 0})
	if result.ProcessErr != nil {
		t.Fatalf("process job: %v", result.ProcessErr)
	}
	if renderer.callCount != 1 {
		t.Fatalf("got renderer calls %d, want 1", renderer.callCount)
	}
	if result.ParsedPage == nil {
		t.Fatalf("expected parsed page but got nil")
	}
	if result.ParsedPage.Title != "" {
		t.Fatalf("expected raw shell title to remain empty, got %q", result.ParsedPage.Title)
	}
}

func TestExtractRenderedHTMLStripsLeadingNoise(t *testing.T) {
	output := []byte("warning line\n<!DOCTYPE html><html><body>Hello</body></html>")
	renderedHTML := extractRenderedHTML(output)
	if string(renderedHTML) != "<!DOCTYPE html><html><body>Hello</body></html>" {
		t.Fatalf("got rendered html %q", string(renderedHTML))
	}
}
