package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Fetcher performs plain HTTP requests for crawl targets.
type Fetcher struct {
	httpClient *http.Client
	userAgent  string
}

// NewFetcher builds a fetcher with the provided settings.
// The fetcher uses a transport that blocks connections to private/reserved IPs
// (SSRF guard) and a CheckRedirect hook that re-validates each redirect target.
func NewFetcher(fetchTimeout time.Duration, userAgent string) *Fetcher {
	return &Fetcher{
		httpClient: &http.Client{
			Timeout:       fetchTimeout,
			Transport:     newSafeTransport(),
			CheckRedirect: safeCheckRedirect,
		},
		userAgent: userAgent,
	}
}

// newFetcherWithClient builds a fetcher that uses the supplied http.Client as-is.
// This is intended for tests that need to talk to loopback servers (httptest)
// and must not be used in production paths.
func newFetcherWithClient(client *http.Client, userAgent string) *Fetcher {
	return &Fetcher{
		httpClient: client,
		userAgent:  userAgent,
	}
}

// Fetch requests one URL and returns the raw HTTP response details.
func (fetcher *Fetcher) Fetch(ctx context.Context, targetURL string) FetchResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return FetchResult{FetchError: fmt.Errorf("build request: %w", err)}
	}

	if fetcher.userAgent != "" {
		request.Header.Set("User-Agent", fetcher.userAgent)
	}

	startedAt := time.Now()
	response, err := fetcher.httpClient.Do(request)
	if err != nil {
		return FetchResult{FetchError: fmt.Errorf("fetch url: %w", err)}
	}
	defer response.Body.Close()

	// C-4: cap the response body to avoid unbounded memory use.
	limitedReader := io.LimitReader(response.Body, maxBodyBytes+1)
	responseBody, err := io.ReadAll(limitedReader)
	if err != nil {
		return FetchResult{FetchError: fmt.Errorf("read response body: %w", err)}
	}
	if int64(len(responseBody)) > maxBodyBytes {
		return FetchResult{FetchError: fmt.Errorf("response body exceeds %d-byte limit", maxBodyBytes)}
	}

	return FetchResult{
		FinalURL:     response.Request.URL.String(),
		StatusCode:   response.StatusCode,
		ContentType:  response.Header.Get("Content-Type"),
		Body:         responseBody,
		ResponseTime: time.Since(startedAt),
		ResponseSize: len(responseBody),
	}
}
