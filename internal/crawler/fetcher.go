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
func NewFetcher(fetchTimeout time.Duration, userAgent string) *Fetcher {
	return &Fetcher{
		httpClient: &http.Client{Timeout: fetchTimeout},
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

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return FetchResult{FetchError: fmt.Errorf("read response body: %w", err)}
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
