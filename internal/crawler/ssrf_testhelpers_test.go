package crawler

import "testing"

// allowLoopbackDialsForTest lets a test's fetcher/renderer dial 127.0.0.1 so it
// can exercise httptest.Server fixtures despite the SSRF loopback guard. The
// guard is restored once the test completes.
func allowLoopbackDialsForTest(t *testing.T) {
	t.Helper()
	allowLoopbackForTests.Store(true)
	t.Cleanup(func() { allowLoopbackForTests.Store(false) })
}
