package crawler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// blockedIP returns a non-nil error if ip must not be dialled (SSRF guard).
// Covers: loopback, link-local unicast/multicast, private (RFC1918), CGNAT
// (100.64/10), unspecified (0.0.0.0 / ::), ULA (fc00::/7), and the AWS/GCP
// instance-metadata address 169.254.169.254.
func blockedIP(ip net.IP) error {
	if ip.IsLoopback() {
		return fmt.Errorf("ssrf: loopback address %s is not allowed", ip)
	}
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("ssrf: link-local address %s is not allowed", ip)
	}
	if ip.IsLinkLocalMulticast() {
		return fmt.Errorf("ssrf: link-local multicast address %s is not allowed", ip)
	}
	if ip.IsMulticast() {
		return fmt.Errorf("ssrf: multicast address %s is not allowed", ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("ssrf: unspecified address %s is not allowed", ip)
	}
	if ip.IsPrivate() {
		// Covers RFC1918 (10/8, 172.16/12, 192.168/16) and ULA (fc00::/7).
		return fmt.Errorf("ssrf: private address %s is not allowed", ip)
	}

	// CGNAT 100.64.0.0/10 (RFC6598) — not covered by IsPrivate in older Go.
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && (ip4[1]&0xC0) == 64 {
			return fmt.Errorf("ssrf: CGNAT address %s is not allowed", ip)
		}
	}

	return nil
}

// ssrfDialContext resolves the target host, checks every resulting IP against
// the block-list, and then dials the first allowed IP directly (avoiding
// TOCTOU re-resolution by the OS resolver).
func ssrfDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf dial: split host/port: %w", err)
	}

	// If the host is already a literal IP we skip DNS and validate directly.
	if literalIP := net.ParseIP(host); literalIP != nil {
		if err := blockedIP(literalIP); err != nil {
			return nil, err
		}
		// Dial the literal IP directly.
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(literalIP.String(), port))
	}

	// Resolve the hostname.
	resolver := net.DefaultResolver
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf dial: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("ssrf dial: no addresses for %q", host)
	}

	// Validate every resolved IP before dialling any of them.
	for _, ia := range addrs {
		if err := blockedIP(ia.IP); err != nil {
			return nil, err
		}
	}

	// Dial the first resolved IP directly (bypasses OS re-resolution).
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].IP.String(), port))
}

// newSafeTransport returns an *http.Transport whose DialContext rejects
// connections to private/reserved IP ranges (SSRF guard).
func newSafeTransport() *http.Transport {
	return &http.Transport{
		DialContext:           ssrfDialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// safeCheckRedirect validates each redirect target before following it.
// It rejects non-http/https schemes, enforces the same SSRF IP block-list,
// and caps the redirect chain at maxRedirects hops.
func safeCheckRedirect(req *http.Request, via []*http.Request) error {
	const maxRedirects = 5
	if len(via) >= maxRedirects {
		return fmt.Errorf("redirect: stopped after %d redirects", maxRedirects)
	}

	targetURL := req.URL
	if targetURL.Scheme != "http" && targetURL.Scheme != "https" {
		return fmt.Errorf("redirect: unsupported scheme %q", targetURL.Scheme)
	}

	host := targetURL.Hostname()
	if host == "" {
		return fmt.Errorf("redirect: empty host")
	}

	// Re-apply the SSRF guard on the redirect target host.
	if literalIP := net.ParseIP(host); literalIP != nil {
		if err := blockedIP(literalIP); err != nil {
			return fmt.Errorf("redirect: %w", err)
		}
		return nil
	}

	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("redirect: resolve %q: %w", host, err)
	}
	for _, ip := range addrs {
		if err := blockedIP(ip); err != nil {
			return fmt.Errorf("redirect: %w", err)
		}
	}

	return nil
}

// validateURLForFetch is a lightweight pre-flight check used by the renderer
// path (and anywhere we want to gate a URL before handing it to an external
// process).  It parses the URL, enforces http/https, and resolves & checks
// every IP for the declared host.
func validateURLForFetch(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("validate url: parse: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("validate url: unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("validate url: empty host")
	}

	if literalIP := net.ParseIP(host); literalIP != nil {
		return blockedIP(literalIP)
	}

	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("validate url: resolve %q: %w", host, err)
	}
	for _, ip := range addrs {
		if err := blockedIP(ip); err != nil {
			return err
		}
	}
	return nil
}
