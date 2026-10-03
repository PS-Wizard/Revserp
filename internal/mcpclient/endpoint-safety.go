package mcpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errRedirectRefused stops redirect following so a token stays on its endpoint.
var errRedirectRefused = errors.New("redirects are not allowed")

// errBodyTooLarge is matched with errors.Is to classify CodeTooLarge.
var errBodyTooLarge = errors.New("response body exceeds size limit")

// blockedNets holds every range an endpoint may never resolve to: private,
// loopback, link-local, CGNAT, multicast, unspecified, reserved, documentation
// and benchmark space, in both IPv4 and IPv6 form.
var blockedNets []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
		"192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			blockedNets = append(blockedNets, n)
		}
	}
}

// isPublicIP reports whether ip is a globally routable unicast address. IPv4-mapped
// IPv6 forms are normalized first.
func isPublicIP(ip net.IP) bool {
	if len(ip) == 0 {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

// validateBearer rejects empty, overlong and non-ASCII-printable credentials before
// they reach a header, where a newline or space would be malformed.
func validateBearer(token string) error {
	if token == "" {
		return fail(CodeInvalidToken, "bearer token is empty", nil)
	}
	return validateBearerContent(token)
}

// validateBearerOptional accepts an explicitly unauthenticated session:
// empty means no Authorization header is sent. Supplied tokens validate
// exactly as before. Callers decide whether no-auth is permitted.
func validateBearerOptional(token string) error {
	if token == "" {
		return nil
	}
	return validateBearerContent(token)
}

func validateBearerContent(token string) error {
	if len(token) > maxBearerLen {
		return fail(CodeInvalidToken, "bearer token is too long", nil)
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c < 0x21 || c > 0x7e {
			return fail(CodeInvalidToken, "bearer token contains unsupported characters", nil)
		}
	}
	return nil
}

// validateStrictEndpoint enforces the production endpoint policy: HTTP/HTTPS on
// web ports only (80/8080, 443/8443), no embedded credentials, no fragment, and a
// host resolving exclusively to public addresses. DNS uses the caller's context
// bounded by resolveTimeout so validation cannot outlive the connect deadline.
func validateStrictEndpoint(ctx context.Context, endpoint string) (*url.URL, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint is empty", nil)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint is not a valid URL", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if !u.IsAbs() || (scheme != "https" && scheme != "http") {
		return nil, fail(CodeInvalidEndpoint, "endpoint must be an http(s) URL", nil)
	}
	if u.User != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not embed credentials", nil)
	}
	if u.Fragment != "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not contain a fragment", nil)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint has no host", nil)
	}
	port := u.Port()
	okPort := (scheme == "http" && (port == "" || port == "80" || port == "8080")) ||
		(scheme == "https" && (port == "" || port == "443" || port == "8443"))
	if !okPort {
		return nil, fail(CodeInvalidEndpoint, "endpoint uses an unexpected port", nil)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, fail(CodeInvalidEndpoint, "endpoint host is not allowed", nil)
		}
		return u, nil
	}
	rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(rctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fail(CodeUnreachable, "endpoint host cannot be resolved", err)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, fail(CodeInvalidEndpoint, "endpoint host is not allowed", nil)
		}
	}
	return u, nil
}

// validateTestEndpoint pairs with connectWithHTTPClient: http/https on any port,
// so httptest servers resolve, but still no credentials or fragments. Production
// Connect never uses this path.
func validateTestEndpoint(endpoint string) (*url.URL, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint is empty", nil)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint is not a valid URL", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if !u.IsAbs() || (scheme != "https" && scheme != "http") {
		return nil, fail(CodeInvalidEndpoint, "endpoint must be an http(s) URL", nil)
	}
	if u.User != nil {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not embed credentials", nil)
	}
	if u.Fragment != "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint must not contain a fragment", nil)
	}
	if u.Hostname() == "" {
		return nil, fail(CodeInvalidEndpoint, "endpoint has no host", nil)
	}
	return u, nil
}

// safeDialContext dials only validated public addresses. It re-resolves at dial
// time and pins the connection to one validated IP, so DNS cannot rebind between
// validateStrictEndpoint and connect. Any blocked address fails the dial closed.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	switch port {
	case "80", "8080", "443", "8443":
	default:
		return nil, errors.New("mcpclient: blocked port")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("mcpclient: DNS resolution returned no addresses")
		}
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, errors.New("mcpclient: blocked host")
		}
	}
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// productUserAgent names this client; some upstreams answer Go's default
// Go-http-client/1.1 with 406 HTML.
const productUserAgent = "revserp-mcp/1.0"

// bearerTransport injects the bearer token and product user agent on every request
// and caps each body at maxHTTPBodyBytes: MaxEventSize only bounds SSE events, so
// plain JSON needs its own limit. Headers go on a clone, leaving the caller's
// request untouched.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

// cappedBody fails reads with errBodyTooLarge past maxHTTPBodyBytes, so an
// oversized body surfaces as CodeTooLarge instead of buffering without bound.
type cappedBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.rc.Read(p)
	c.remaining -= int64(n)
	if c.remaining <= 0 && err == nil {
		return n, errBodyTooLarge
	}
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	out := r.Clone(r.Context())
	if b.token != "" {
		out.Header.Set("Authorization", "Bearer "+b.token)
	} else {
		// An explicitly unauthenticated session sends no credentials at
		// all, even if the cloned request carried a stale header.
		out.Header.Del("Authorization")
	}
	out.Header.Set("User-Agent", productUserAgent)
	resp, err := b.base.RoundTrip(out)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &cappedBody{rc: resp.Body, remaining: maxHTTPBodyBytes + 1}
	return resp, nil
}

// CloseIdleConnections forwards cleanup through the wrapper.
func (b *bearerTransport) CloseIdleConnections() {
	if closer, ok := b.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return errRedirectRefused }

// newSafeHTTPClient builds the production client: pinned dialer, no environment
// proxy, TLS 1.2 floor, bearer injection, redirects refused.
func newSafeHTTPClient(token string) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport:     &bearerTransport{base: tr, token: token},
		CheckRedirect: refuseRedirect,
		Timeout:       50 * time.Second,
	}
}
