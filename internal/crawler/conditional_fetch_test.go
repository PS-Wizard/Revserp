package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchConditionalPrefersETagOverLastModified(t *testing.T) {
	allowLoopbackDialsForTest(t)

	var gotIfNoneMatch, gotIfModifiedSince string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotIfNoneMatch = request.Header.Get("If-None-Match")
		gotIfModifiedSince = request.Header.Get("If-Modified-Since")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	fetcher.FetchConditional(context.Background(), server.URL, `"etag-value"`, "Wed, 21 Oct 2015 07:28:00 GMT")

	if gotIfNoneMatch != `"etag-value"` {
		t.Fatalf("got If-None-Match %q, want %q", gotIfNoneMatch, `"etag-value"`)
	}

	if gotIfModifiedSince != "" {
		t.Fatalf("got If-Modified-Since %q, want empty: RFC 9110 requires ignoring it when If-None-Match is present", gotIfModifiedSince)
	}
}

func TestFetchConditionalSendsLastModifiedWhenETagEmpty(t *testing.T) {
	allowLoopbackDialsForTest(t)

	const lastModified = "Wed, 21 Oct 2015 07:28:00 GMT"
	var gotIfNoneMatch, gotIfModifiedSince string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotIfNoneMatch = request.Header.Get("If-None-Match")
		gotIfModifiedSince = request.Header.Get("If-Modified-Since")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	fetcher.FetchConditional(context.Background(), server.URL, "", lastModified)

	if gotIfModifiedSince != lastModified {
		t.Fatalf("got If-Modified-Since %q, want %q", gotIfModifiedSince, lastModified)
	}

	if gotIfNoneMatch != "" {
		t.Fatalf("got If-None-Match %q, want empty", gotIfNoneMatch)
	}
}

func TestFetchConditionalSendsNoValidatorsWhenBothEmpty(t *testing.T) {
	allowLoopbackDialsForTest(t)

	var gotIfNoneMatch, gotIfModifiedSince string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotIfNoneMatch = request.Header.Get("If-None-Match")
		gotIfModifiedSince = request.Header.Get("If-Modified-Since")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	fetcher.FetchConditional(context.Background(), server.URL, "", "")

	if gotIfNoneMatch != "" {
		t.Fatalf("got If-None-Match %q, want empty", gotIfNoneMatch)
	}

	if gotIfModifiedSince != "" {
		t.Fatalf("got If-Modified-Since %q, want empty", gotIfModifiedSince)
	}
}

func TestFetchConditionalEchoesLastModifiedByteForByte(t *testing.T) {
	allowLoopbackDialsForTest(t)

	// A legacy RFC 850 date, deliberately not in the canonical RFC 1123 form a
	// parse-and-reformat round trip would produce.
	const oddLastModified = "Saturday, 25-Jul-26 21:36:16 GMT"
	var gotIfModifiedSince string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotIfModifiedSince = request.Header.Get("If-Modified-Since")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	fetcher.FetchConditional(context.Background(), server.URL, "", oddLastModified)

	if gotIfModifiedSince != oddLastModified {
		t.Fatalf("got If-Modified-Since %q, want exact echo %q", gotIfModifiedSince, oddLastModified)
	}
}

func TestFetchConditionalReturnsNotModifiedResult(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"v2"`)
		writer.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		writer.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	result := fetcher.FetchConditional(context.Background(), server.URL, `"v1"`, "")

	if !result.NotModified {
		t.Fatalf("expected NotModified true")
	}

	if result.StatusCode != http.StatusNotModified {
		t.Fatalf("got status %d, want %d", result.StatusCode, http.StatusNotModified)
	}

	if len(result.Body) != 0 {
		t.Fatalf("got body %q, want empty", result.Body)
	}

	if result.ETag != `"v2"` {
		t.Fatalf("got ETag %q, want %q", result.ETag, `"v2"`)
	}

	if result.LastModified != "Wed, 21 Oct 2015 07:28:00 GMT" {
		t.Fatalf("got Last-Modified %q, want %q", result.LastModified, "Wed, 21 Oct 2015 07:28:00 GMT")
	}
}

func TestFetchConditionalDoesNotRetryNotModified(t *testing.T) {
	allowLoopbackDialsForTest(t)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 3, time.Millisecond, 10*time.Millisecond)
	result := fetcher.FetchConditional(context.Background(), server.URL, `"v1"`, "")

	if !result.NotModified {
		t.Fatalf("expected NotModified true")
	}

	if requestCount != 1 {
		t.Fatalf("got %d requests, want 1: a 304 must not be treated as a retryable throttle signal", requestCount)
	}
}

func TestFetcherWithMaxBodyBytesRejectsOversizedBody(t *testing.T) {
	allowLoopbackDialsForTest(t)

	const bodySize = 5000
	oversizedBody := strings.Repeat("a", bodySize)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, oversizedBody)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second).WithMaxBodyBytes(1024)
	result := fetcher.Fetch(context.Background(), server.URL)

	if result.FetchError == nil {
		t.Fatalf("expected fetch error for oversized body, got nil (body len %d)", len(result.Body))
	}

	if !strings.Contains(result.FetchError.Error(), "limit") {
		t.Fatalf("got error %q, want it to mention the limit", result.FetchError)
	}

	if len(result.Body) != 0 {
		t.Fatalf("got body of length %d, want no truncated body returned", len(result.Body))
	}
}

func TestFetcherWithMaxBodyBytesZeroRestoresDefault(t *testing.T) {
	allowLoopbackDialsForTest(t)

	const bodySize = 5000
	body := strings.Repeat("a", bodySize)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, body)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second).WithMaxBodyBytes(1024).WithMaxBodyBytes(0)
	result := fetcher.Fetch(context.Background(), server.URL)

	if result.FetchError != nil {
		t.Fatalf("fetch: %v", result.FetchError)
	}

	if len(result.Body) != bodySize {
		t.Fatalf("got body length %d, want %d", len(result.Body), bodySize)
	}
}
