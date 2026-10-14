package crawler

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxExcludedURLPatterns      = 100
	MaxExcludedURLPatternLength = 2048
)

// NormalizeExcludedURLPatterns trims, dedupes, and validates raw exclusion
// patterns. A pattern is either a full http(s) URL or a root-relative path
// starting with /. Only * is a wildcard; every other character is literal.
// Full URLs match the full canonical URL (scheme, host, decoded path, raw
// query). Path-only rules match the URL pathname only, so query and fragment
// syntax is rejected there: it would be accepted but could never match.
// Percent-escapes in patterns are decoded before matching, so an encoded
// pattern and a literal pattern for the same path are equivalent.
func NormalizeExcludedURLPatterns(patterns []string) ([]string, error) {
	normalized := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if utf8.RuneCountInString(trimmed) > MaxExcludedURLPatternLength {
			return nil, fmt.Errorf("excluded pattern %q is longer than %d characters", trimmed, MaxExcludedURLPatternLength)
		}
		if strings.ContainsFunc(trimmed, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, fmt.Errorf("excluded pattern %q must not contain whitespace or control characters", trimmed)
		}
		if strings.Contains(trimmed, "#") {
			return nil, fmt.Errorf("excluded pattern %q must not contain a fragment: fragments are stripped before matching", trimmed)
		}
		if strings.HasPrefix(trimmed, "/") {
			if err := validateExcludedPathPattern(trimmed); err != nil {
				return nil, err
			}
		} else if err := validateExcludedFullURLPattern(trimmed); err != nil {
			return nil, err
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) > MaxExcludedURLPatterns {
		return nil, fmt.Errorf("excluded patterns: got %d, maximum is %d", len(normalized), MaxExcludedURLPatterns)
	}
	return normalized, nil
}

func validateExcludedPathPattern(pattern string) error {
	if strings.HasPrefix(pattern, "//") {
		return fmt.Errorf("excluded pattern %q must be a full http(s) URL or a root-relative path starting with /", pattern)
	}
	if strings.Contains(pattern, "?") {
		return fmt.Errorf("excluded pattern %q must not contain a query: path-only rules match the pathname independent of query", pattern)
	}
	parsed, err := url.Parse(pattern)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" {
		return fmt.Errorf("excluded pattern %q must be a full http(s) URL or a root-relative path starting with /", pattern)
	}
	return nil
}

func validateExcludedFullURLPattern(pattern string) error {
	parsed, err := url.Parse(pattern)
	if err != nil {
		return fmt.Errorf("excluded pattern %q is not a valid URL: %v", pattern, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("excluded pattern %q must use http or https, or be a root-relative path starting with /", pattern)
	}
	if parsed.Host == "" {
		return fmt.Errorf("excluded pattern %q must contain a host", pattern)
	}
	return nil
}

// excludedURLRule is one compiled pattern: the raw text split on * so
// matching needs no regexp. bareParts covers a trailing /*, which also
// excludes the pattern with that suffix removed (the bare directory).
// A nil *ExcludedURLMatcher means no exclusions.
type excludedURLRule struct {
	fullURL   bool
	parts     []string
	bareParts []string
}

// ExcludedURLMatcher reports whether a crawl URL is excluded. Compile once
// per crawl with CompileExcludedURLPatterns and share it across the frontier,
// the fetcher, and the probes.
type ExcludedURLMatcher struct {
	rules []excludedURLRule
}

// CompileExcludedURLPatterns validates and compiles patterns. It returns a
// nil matcher when there is nothing to exclude, so callers keep working
// without nil checks: a nil matcher excludes nothing.
func CompileExcludedURLPatterns(patterns []string) (*ExcludedURLMatcher, error) {
	normalized, err := NormalizeExcludedURLPatterns(patterns)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	matcher := &ExcludedURLMatcher{rules: make([]excludedURLRule, 0, len(normalized))}
	for _, pattern := range normalized {
		fullURL := !strings.HasPrefix(pattern, "/")
		rule := excludedURLRule{fullURL: fullURL}
		rule.parts = compileExcludedPatternBody(fullURL, canonicalizeExcludedPatternBody(fullURL, pattern))
		if bare, ok := strings.CutSuffix(pattern, "/*"); ok {
			// A trailing /* also excludes the bare directory itself: /blog/*
			// and https://example.com/blog/* both match /blog with no
			// trailing slash. Matching the stripped body as a second pattern
			// also covers wildcards before the suffix (/a/*/leaf/* matches
			// /a/b/leaf).
			rule.bareParts = compileExcludedPatternBody(fullURL, canonicalizeExcludedPatternBody(fullURL, bare))
		}
		matcher.rules = append(matcher.rules, rule)
	}
	return matcher, nil
}

// canonicalizeExcludedPatternBody applies the target canonical form to a
// full-URL pattern body (lowercase scheme and host, empty path becomes /).
// Path-only bodies pass through unchanged.
func canonicalizeExcludedPatternBody(fullURL bool, body string) string {
	if !fullURL {
		return body
	}
	return canonicalExcludedFullURLPattern(body)
}

// compileExcludedPatternBody splits one pattern body on * and percent-decodes
// the path portion of each segment with url.PathUnescape, so encoded and
// literal patterns for the same path compile identically. The query portion
// of full URLs stays raw: queries match the canonical raw query. A segment
// with an invalid escape stays literal.
func compileExcludedPatternBody(fullURL bool, body string) []string {
	if !fullURL {
		return unescapeExcludedPatternSegments(strings.Split(body, "*"))
	}
	pathBody := body
	queryBody := ""
	if i := strings.Index(body, "?"); i >= 0 {
		pathBody = body[:i]
		queryBody = body[i:]
	}
	segments := unescapeExcludedPatternSegments(strings.Split(pathBody, "*"))
	if queryBody == "" {
		return segments
	}
	segments[len(segments)-1] += queryBody
	last := strings.Split(segments[len(segments)-1], "*")
	return append(segments[:len(segments)-1], last...)
}

// unescapeExcludedPatternSegments decodes one percent-escape layer per
// segment. Segments are split on * first, so a decoded %2A cannot become
// a new wildcard.
func unescapeExcludedPatternSegments(segments []string) []string {
	for i, segment := range segments {
		if decoded, err := url.PathUnescape(segment); err == nil {
			segments[i] = decoded
		}
	}
	return segments
}

// canonicalExcludedFullURLPattern applies the same canonical form NormalizeURL
// gives targets (lowercase scheme and host, empty path becomes /) so a
// pattern written with an uppercase host still matches. The path and query
// keep their case: paths match case-sensitively.
func canonicalExcludedFullURLPattern(pattern string) string {
	schemeEnd := strings.Index(pattern, "://")
	if schemeEnd < 0 {
		return pattern
	}
	var sb strings.Builder
	sb.WriteString(strings.ToLower(pattern[:schemeEnd]))
	sb.WriteString("://")
	rest := pattern[schemeEnd+3:]
	authority := rest
	tail := ""
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority = rest[:i]
		tail = rest[i:]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		sb.WriteString(authority[:at+1])
		sb.WriteString(strings.ToLower(authority[at+1:]))
	} else {
		sb.WriteString(strings.ToLower(authority))
	}
	if tail == "" || strings.HasPrefix(tail, "?") {
		sb.WriteString("/")
	}
	sb.WriteString(tail)
	return sb.String()
}

// Excludes reports whether targetURL matches any pattern. Full-URL rules
// match scheme, host, decoded path, and raw query; path-only rules match the
// decoded pathname, independent of query. Targets that fail normalization
// are never excluded.
func (matcher *ExcludedURLMatcher) Excludes(targetURL string) bool {
	if matcher == nil || len(matcher.rules) == 0 {
		return false
	}
	normalized, err := NormalizeURL(targetURL, nil)
	if err != nil {
		return false
	}
	for _, rule := range matcher.rules {
		compared := normalized.Path
		if rule.fullURL {
			compared = decodedExcludedTargetURL(normalized)
		}
		if matchExcludedWildcard(rule.parts, compared) || matchExcludedWildcard(rule.bareParts, compared) {
			return true
		}
	}
	return false
}

// decodedExcludedTargetURL renders the target in the same space full-URL
// patterns compile to: lowercase scheme and host, decoded path, raw query.
func decodedExcludedTargetURL(normalized *url.URL) string {
	var sb strings.Builder
	sb.WriteString(normalized.Scheme)
	sb.WriteString("://")
	if normalized.User != nil {
		sb.WriteString(normalized.User.String())
		sb.WriteString("@")
	}
	sb.WriteString(normalized.Host)
	sb.WriteString(normalized.Path)
	if normalized.RawQuery != "" {
		sb.WriteString("?")
		sb.WriteString(normalized.RawQuery)
	}
	return sb.String()
}

// matchExcludedWildcard matches split-on-* parts against s. * spans any
// characters including separators. With no * this is exact equality, so
// exact full URLs are never prefix matches.
func matchExcludedWildcard(parts []string, s string) bool {
	if len(parts) == 0 {
		return false
	}
	if len(parts) == 1 {
		return s == parts[0]
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	rest := s[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, part)
		if i < 0 {
			return false
		}
		rest = rest[i+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// RootExcluded reports whether the crawl root itself is excluded. The runner
// and the create-crawl handler share this so an excluded root is rejected
// before any request is made.
func (config CrawlerConfig) RootExcluded(rootURL string) bool {
	return config.ExcludedURLs.Excludes(rootURL)
}
