package crawler

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
)

// allowLoopbackForTests disables the loopback check in isDisallowedIP. It exists
// solely so this package's tests can exercise the fetcher/renderer against local
// httptest.Server fixtures (which bind to 127.0.0.1); it must never be set by
// production code. Toggled via allowLoopbackDialsForTest in tests.
var allowLoopbackForTests atomic.Bool

// isDisallowedIP reports whether ip must never be dialed by the crawler:
// loopback, unspecified, link-local (incl. 169.254.0.0/16 and fe80::/10),
// RFC1918 private space, unique-local IPv6 (fc00::/7), or multicast.
func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() && !allowLoopbackForTests.Load() {
		return true
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsMulticast() {
		return true
	}
	// net.IP.IsPrivate covers RFC1918 and fc00::/7 (ULA) on modern Go, but keep
	// an explicit ULA check as defense in depth against stdlib behavior changes.
	if ip4 := ip.To4(); ip4 == nil {
		if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
			return true
		}
	}
	return false
}

// ValidatePublicHost resolves host and rejects it if any resolved IP is
// loopback, link-local, private, unique-local, or multicast. This guards
// against SSRF via user-supplied crawl targets (e.g. cloud metadata,
// internal services, RFC1918 hosts).
func ValidatePublicHost(ctx context.Context, host string) error {
	// A bare IP literal in the host (e.g. "169.254.169.254" or "[::1]") is not
	// resolved by LookupIP as a name, but ParseIP still recognizes it directly.
	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedIP(ip) {
			return fmt.Errorf("host %s resolves to a disallowed address", host)
		}
		return nil
	}

	resolver := &net.Resolver{}
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve host %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %s did not resolve to any address", host)
	}

	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("host %s resolves to a disallowed address (%s)", host, ip)
		}
	}

	return nil
}
