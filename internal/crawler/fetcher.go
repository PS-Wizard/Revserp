package crawler

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Fetcher performs plain HTTP requests for crawl targets and transparently
// retries 429/503 responses with exponential backoff and a shared per-host
// cooldown so concurrent workers don't hammer a rate-limited origin in a herd.
type Fetcher struct {
	httpClient        *http.Client
	userAgent         string
	maxRetries        int
	retryBase         time.Duration
	retryMax          time.Duration
	cooldownMu        sync.Mutex
	hostCooldownUntil map[string]time.Time
}

// NewFetcher builds a fetcher with the provided settings.
// maxRetries is the number of retries after the first attempt (total attempts = 1 + maxRetries).
// If maxRetries < 0 it is treated as 0. If retryBase <= 0 it defaults to 1s.
// If retryMax <= 0 it defaults to 15s.
func NewFetcher(fetchTimeout time.Duration, userAgent string, maxRetries int, retryBase time.Duration, retryMax time.Duration) *Fetcher {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if retryBase <= 0 {
		retryBase = time.Second
	}
	if retryMax <= 0 {
		retryMax = 15 * time.Second
	}
	return &Fetcher{
		httpClient:        &http.Client{Timeout: fetchTimeout},
		userAgent:         userAgent,
		maxRetries:        maxRetries,
		retryBase:         retryBase,
		retryMax:          retryMax,
		hostCooldownUntil: make(map[string]time.Time),
	}
}

// Fetch requests one URL, retrying on 429/503 responses with exponential
// backoff and a shared per-host cooldown. Network errors are not retried.
func (fetcher *Fetcher) Fetch(ctx context.Context, targetURL string) FetchResult {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" {
		// Unparseable URL: attempt once and return whatever we get.
		return fetcher.doFetch(ctx, targetURL)
	}
	host := parsed.Hostname()

	for attempt := 0; ; attempt++ {
		// Honor any active shared cooldown for this host before firing the request.
		if wait := fetcher.cooldownRemaining(host); wait > 0 {
			if !sleepCtx(ctx, wait) {
				return FetchResult{FetchError: ctx.Err()}
			}
		}

		result := fetcher.doFetch(ctx, targetURL)

		// Network errors are not retried — they indicate a connectivity problem,
		// not a server-side rate limit.
		if result.FetchError != nil {
			return result
		}

		// Only 429 (rate-limited) and 503 (service unavailable / Cloudflare challenge)
		// are treated as retryable throttle signals.
		if result.StatusCode != 429 && result.StatusCode != 503 {
			return result
		}

		if attempt >= fetcher.maxRetries {
			// Retries exhausted; return the throttled result so the status-gate
			// above can skip it, matching the existing non-2xx skip behavior.
			return result
		}

		wait := fetcher.backoffDuration(result, attempt)
		fetcher.setCooldown(host, wait)
		if !sleepCtx(ctx, wait) {
			return FetchResult{FetchError: ctx.Err()}
		}
	}
}

// doFetch performs a single HTTP GET and captures the full response.
// The Retry-After header value is preserved on FetchResult so the retry loop
// can honour explicit server-provided delays.
func (fetcher *Fetcher) doFetch(ctx context.Context, targetURL string) FetchResult {
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
		RetryAfter:   response.Header.Get("Retry-After"),
		Body:         responseBody,
		ResponseTime: time.Since(startedAt),
		ResponseSize: len(responseBody),
	}
}

// sleepCtx sleeps for d, returning true on completion or false if the context
// is canceled first. A non-positive duration is a no-op that returns true.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// cooldownRemaining returns the remaining cooldown duration for host, or 0 if
// none is active. The caller must not be holding cooldownMu.
func (fetcher *Fetcher) cooldownRemaining(host string) time.Duration {
	fetcher.cooldownMu.Lock()
	defer fetcher.cooldownMu.Unlock()
	until, ok := fetcher.hostCooldownUntil[host]
	if !ok {
		return 0
	}
	remaining := time.Until(until)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// setCooldown records a per-host cooldown, extending an existing later deadline
// only if the new deadline is further in the future. This prevents a fast
// worker from shrinking a cooldown that a slower sibling already set.
func (fetcher *Fetcher) setCooldown(host string, d time.Duration) {
	if d <= 0 {
		return
	}
	newUntil := time.Now().Add(d)
	fetcher.cooldownMu.Lock()
	defer fetcher.cooldownMu.Unlock()
	if existing, ok := fetcher.hostCooldownUntil[host]; !ok || newUntil.After(existing) {
		fetcher.hostCooldownUntil[host] = newUntil
	}
}

// backoffDuration computes how long to wait before the next attempt.
// If the response carries a Retry-After header it is honoured (delta-seconds
// or HTTP-date), capped at retryMax. Otherwise full-jitter exponential backoff
// is used: a random duration in [0, min(retryMax, retryBase*2^attempt)].
func (fetcher *Fetcher) backoffDuration(result FetchResult, attempt int) time.Duration {
	if result.RetryAfter != "" {
		// Try delta-seconds first (the common Retry-After format).
		if secs, err := strconv.ParseInt(result.RetryAfter, 10, 64); err == nil {
			d := time.Duration(secs) * time.Second
			if d > fetcher.retryMax {
				d = fetcher.retryMax
			}
			return d
		}
		// Fall back to HTTP-date format.
		if t, err := http.ParseTime(result.RetryAfter); err == nil {
			d := time.Until(t)
			if d < 0 {
				d = 0
			}
			if d > fetcher.retryMax {
				d = fetcher.retryMax
			}
			return d
		}
	}

	// Full-jitter exponential backoff: pick uniformly from [0, ceiling].
	// Cap the shift to avoid int64 overflow on large attempt counts.
	const maxShift = 62
	shift := attempt
	if shift > maxShift {
		shift = maxShift
	}
	ceiling := fetcher.retryBase * (1 << uint(shift))
	// Clamp ceiling without overflow: if it went negative due to overflow or
	// exceeds retryMax, pin it to retryMax.
	if ceiling <= 0 || ceiling > fetcher.retryMax {
		ceiling = fetcher.retryMax
	}
	n := rand.Int63n(int64(ceiling) + 1) //nolint:gosec // non-cryptographic jitter
	return time.Duration(n)
}
