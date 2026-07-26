package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProcessJobParsesHTML(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `
		<!DOCTYPE html>
		<html lang="en">
		<head>
			<title>Worker Test</title>
			<meta name="description" content="Worker description">
		</head>
		<body>
			<h1>Hello World</h1>
			<a href="/about">About</a>
		</body>
		</html>
		`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	result := ProcessJob(context.Background(), fetcher, parser, nil, CrawlJob{URL: server.URL, Depth: 0})

	if result.ProcessErr != nil {
		t.Fatalf("process job: %v", result.ProcessErr)
	}

	if result.ParsedPage == nil {
		t.Fatalf("expected parsed page but got nil")
	}

	if result.ParsedPage.Title != "Worker Test" {
		t.Fatalf("got title %q", result.ParsedPage.Title)
	}

	if len(result.ParsedPage.Links) != 1 {
		t.Fatalf("got %d links", len(result.ParsedPage.Links))
	}
}

func TestProcessJobSkipsNonHTML(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(writer, "not html")
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	result := ProcessJob(context.Background(), fetcher, parser, nil, CrawlJob{URL: server.URL, Depth: 0})

	if result.ProcessErr != nil {
		t.Fatalf("process job: %v", result.ProcessErr)
	}

	if result.ParsedPage != nil {
		t.Fatalf("expected parsed page to be nil for non-html content")
	}

	if result.Fetch.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", result.Fetch.StatusCode)
	}
}

func TestProcessJobReturnsNotModifiedWithoutParsing(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusNotModified)
		// Some origins incorrectly send a body alongside 304; ProcessJob must not parse it.
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>should not be parsed</title></head></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	result := ProcessJob(context.Background(), fetcher, parser, nil, CrawlJob{URL: server.URL, Depth: 0, ETag: `"v1"`})

	if result.ProcessErr != nil {
		t.Fatalf("process job: %v", result.ProcessErr)
	}

	if !result.NotModified {
		t.Fatalf("expected NotModified true")
	}

	if result.ParsedPage != nil {
		t.Fatalf("expected parsed page to be nil for a 304 response")
	}
}

func TestProcessJobParsesFreshResponseAndNotModifiedIsFalse(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>fresh</title></head><body></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	result := ProcessJob(context.Background(), fetcher, parser, nil, CrawlJob{URL: server.URL, Depth: 0})

	if result.ProcessErr != nil {
		t.Fatalf("process job: %v", result.ProcessErr)
	}

	if result.NotModified {
		t.Fatalf("expected NotModified false for a fresh 200 response")
	}

	if result.ParsedPage == nil {
		t.Fatalf("expected parsed page but got nil")
	}
}
