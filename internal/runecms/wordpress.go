// WordPress transport profile: ConnectWordPress is Connect with the
// WordPress implementation name.
//
// The same hardened HTTP client, endpoint policy, bearer handling, size caps
// and single-attempt call semantics apply. Tool exposure is dynamic: every
// advertised tool the session validates is kept, so enabling a capability
// group or adding a server tool never fails the connection. Filesystem, raw
// SQL/database and batch tools stay excluded (see exposureExcluded), and the
// catalogue in internal/aichattools decides approval policy and local
// descriptions, never which names may run.
package runecms

import (
	"context"
	"net/http"
	"time"
)

// ConnectWordPress opens a WordPress MCP session: MCP initialization plus
// tools/list pagination. It never executes tools. It is Connect with the
// WordPress profile name, not a second transport implementation.
func ConnectWordPress(ctx context.Context, endpoint, bearerToken string) (*Session, error) {
	if err := validateBearer(bearerToken); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if _, err := validateStrictEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	return connectInner(ctx, endpoint, newSafeHTTPClient(bearerToken), "wordpress")
}

// connectWordPressWithHTTPClient is the test-only injection point for
// ConnectWordPress, mirroring connectWithHTTPClient. It is unexported so
// production code cannot reach private networks through it.
func connectWordPressWithHTTPClient(ctx context.Context, endpoint, bearerToken string, hc *http.Client) (*Session, error) {
	if err := validateBearer(bearerToken); err != nil {
		return nil, err
	}
	if _, err := validateTestEndpoint(endpoint); err != nil {
		return nil, err
	}
	base := http.DefaultTransport
	var timeout time.Duration
	if hc != nil {
		if hc.Transport != nil {
			base = hc.Transport
		}
		timeout = hc.Timeout
	}
	cp := &http.Client{
		Transport:     &bearerTransport{base: base, token: bearerToken},
		CheckRedirect: refuseRedirect,
		Timeout:       timeout,
	}
	return connectInner(ctx, endpoint, cp, "wordpress")
}
