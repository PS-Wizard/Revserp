package crawler

import (
	"net/http"
	"time"
)

// newTestFetcher returns a Fetcher that uses a plain http.Client with no SSRF
// guard.  Tests need this because httptest.NewServer binds to 127.0.0.1, which
// the production SSRF dialer correctly rejects.  Never use outside tests.
func newTestFetcher(timeout time.Duration, userAgent string) *Fetcher {
	return newFetcherWithClient(&http.Client{Timeout: timeout}, userAgent)
}
