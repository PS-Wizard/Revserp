package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartWorkerPoolProcessesJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(writer, `<!DOCTYPE html>
							<html lang="en">
							<head><title>%s</title></head>
							<body><h1>%s</h1></body>
							</html>`,
			request.URL.Path, request.URL.Path)
	}))
	defer server.Close()

	jobs := make(chan CrawlJob, 3)
	jobs <- CrawlJob{URL: server.URL + "/one", Depth: 0}
	jobs <- CrawlJob{URL: server.URL + "/two", Depth: 1}
	jobs <- CrawlJob{URL: server.URL + "/three", Depth: 2}
	close(jobs)

	fetcher := newTestFetcher(5*time.Second, "")
	parser := NewParser()
	results := StartWorkerPool(context.Background(), 2, fetcher, parser, nil, jobs)

	resultCount := 0
	seenTitles := map[string]bool{}

	for result := range results {
		if result.ProcessErr != nil {
			t.Fatalf("process job: %v", result.ProcessErr)
		}

		if result.ParsedPage == nil {
			t.Fatalf("expected parsed page but got nil")
		}

		seenTitles[result.ParsedPage.Title] = true
		resultCount++
	}

	if resultCount != 3 {
		t.Fatalf("got %d results", resultCount)
	}

	if !seenTitles["/one"] || !seenTitles["/two"] || !seenTitles["/three"] {
		t.Fatalf("missing expected titles: %#v", seenTitles)
	}
}
