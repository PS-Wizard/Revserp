// Package websitescope canonicalizes branch website scope URLs and matches
// stored crawl page URLs against them. A location website audit only ever
// scores same-site pages, so foreign origins never match.
package websitescope

import (
	"errors"
	"net/url"
	"strings"
)

// ScopeMatchNone disables the branch audit. ScopeMatchExact scores one page,
// ScopeMatchSubtree scores that page plus descendant path segments.
const (
	ScopeMatchNone    = "none"
	ScopeMatchExact   = "exact"
	ScopeMatchSubtree = "subtree"
)

// CanonicalizeWebsiteScopeURL normalizes a scope URL to scheme://host/path.
// Query and fragment never affect matching and are dropped. Only absolute
// http/https URLs survive; paths with encoded slashes, encoded dots,
// nested percent encodings, backslashes, or dot segments are rejected
// because exact and subtree matching would read them ambiguously. Legit
// UTF-8 escapes such as %C3%A9 contain none of those forms and still pass.
func CanonicalizeWebsiteScopeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("websitescope: scope url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !parsed.IsAbs() {
		return "", errors.New("websitescope: scope url must be absolute")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("websitescope: scope url must be http or https")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errors.New("websitescope: scope url must have a host")
	}
	if strings.Contains(host, "%") {
		return "", errors.New("websitescope: scope url host must not be encoded")
	}
	// Reject ambiguous encoded path forms before anything else. hasAmbiguousScopePath
	// explains the exact rule; query and fragment are already out of scope here.
	if hasAmbiguousScopePath(parsed.EscapedPath()) {
		return "", errors.New("websitescope: scope url path must not contain encoded slashes, encoded dots, nested percent encodings, backslashes, or dot segments")
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	hostport := host
	if port := parsed.Port(); port != "" && !isDefaultPort(scheme, port) {
		hostport += ":" + port
	}
	return scheme + "://" + hostport + path, nil
}

// MatchWebsiteScopePage reports whether a stored page URL falls inside a
// canonical scope. Query and fragment are ignored on both sides. Pages from
// a foreign origin never match, pages with ambiguous encoded or dot-segment
// paths never match, and ScopeMatchNone never matches.
func MatchWebsiteScopePage(scopeMatch, scopeURL, pageURL string) bool {
	if scopeMatch != ScopeMatchExact && scopeMatch != ScopeMatchSubtree {
		return false
	}
	scope, err := url.Parse(scopeURL)
	if err != nil || scope.Hostname() == "" {
		return false
	}
	page, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || page.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(scope.Scheme, page.Scheme) || !strings.EqualFold(scope.Hostname(), page.Hostname()) {
		return false
	}
	if normalizedPort(scope) != normalizedPort(page) {
		return false
	}
	scopePath := normalizeScopeMatchPath(scope.EscapedPath())
	pagePath := normalizeScopeMatchPath(page.EscapedPath())
	// Read-time guard: an encoded dot or nested encoding in a stored page
	// could decode to a dot segment outside the branch, so such pages never
	// match. Scopes written before this guard existed get the same check.
	if hasAmbiguousScopePath(scopePath) || hasAmbiguousScopePath(pagePath) {
		return false
	}
	if scopeMatch == ScopeMatchExact {
		return scopePath == pagePath
	}
	if pagePath == scopePath {
		return true
	}
	if scopePath == "/" {
		return strings.HasPrefix(pagePath, "/")
	}
	return strings.HasPrefix(pagePath, scopePath+"/")
}

// normalizeScopeMatchPath folds a stored page path exactly the way
// CanonicalizeWebsiteScopeURL folds a scope path: empty becomes root and a
// trailing slash falls away. The ambiguity guard always runs on the
// untrimmed escaped path first, so trimming here cannot smuggle an encoded
// slash, dot, or traversal past it.
func normalizeScopeMatchPath(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	if len(escapedPath) > 1 {
		return strings.TrimSuffix(escapedPath, "/")
	}
	return escapedPath
}

// ValidScopeMatch reports whether match is a known scope match value.
func ValidScopeMatch(match string) bool {
	return match == ScopeMatchNone || match == ScopeMatchExact || match == ScopeMatchSubtree
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

func normalizedPort(u *url.URL) string {
	if port := u.Port(); port != "" && !isDefaultPort(strings.ToLower(u.Scheme), port) {
		return port
	}
	return ""
}

// ScopeMatchesParentOrigin reports whether a canonical scope URL shares its
// origin with the parent project website. The branch audit only ever scores
// same origin pages, so a foreign scope URL is rejected before anything
// persists. Scheme, host and port normalize exactly as in matching, which
// means http and https are different origins and default ports fold away.
func ScopeMatchesParentOrigin(scopeURL, parentBaseURL string) bool {
	scope, err := url.Parse(scopeURL)
	if err != nil || scope.Hostname() == "" {
		return false
	}
	parent, err := url.Parse(strings.TrimSpace(parentBaseURL))
	if err != nil || parent.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(scope.Scheme, parent.Scheme) || !strings.EqualFold(scope.Hostname(), parent.Hostname()) {
		return false
	}
	return normalizedPort(scope) == normalizedPort(parent)
}

// hasAmbiguousScopePath reports escaped-path forms that exact and subtree
// matching would read ambiguously, so both reject them. The precise rule:
// any encoded slash (%2f), encoded backslash (%5c), encoded dot (%2e), or
// encoded percent (%25) in any letter case, plus literal backslashes and
// dot segments. %25 covers every nested double encoding (%252f, %252e,
// %255c) since each contains an encoded percent, and %2e covers the
// encoded-dot traversal family. Matching is case-insensitive on the hex
// digits, so mixed case like %2F or %2E rejects too. Query and fragment
// are dropped before this ever runs, so % there is unaffected, and legit
// UTF-8 escapes such as %C3%A9 contain none of these forms and still pass.
func hasAmbiguousScopePath(escapedPath string) bool {
	lower := strings.ToLower(escapedPath)
	for _, form := range []string{"%2f", "%5c", "%2e", "%25"} {
		if strings.Contains(lower, form) {
			return true
		}
	}
	if strings.Contains(escapedPath, "\\") {
		return true
	}
	for _, segment := range strings.Split(escapedPath, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
