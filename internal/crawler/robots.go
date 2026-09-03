package crawler

import (
	"context"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// robotsRule is one Allow/Disallow path pattern compiled for matching.
type robotsRule struct {
	// raw is the original pattern text; length excludes the trailing "$"
	// anchor so the longest-match rule compares actual path prefixes.
	raw    string
	re     *regexp.Regexp
	length int
}

// RobotsRules holds the parsed rules from one robots.txt that apply to the
// `*` user-agent group, plus its Crawl-delay. The zero value allows everything.
type RobotsRules struct {
	disallow   []robotsRule
	allow      []robotsRule
	CrawlDelay time.Duration
}

// robotsPatternToRegexp translates one robots.txt path pattern into a regexp.
// `*` matches any sequence of characters and a trailing `$` anchors the end.
// ponytail: only these two wildcard features are supported (the common ones);
// full RFC 9309 wildcard semantics (e.g. mid-pattern `$`, multiple `*`) are not
// implemented — patterns are matched literally except for `*` and trailing `$`.
func robotsPatternToRegexp(pattern string) *regexp.Regexp {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}

	var sb strings.Builder
	sb.Grow(len(pattern) + 8)
	sb.WriteString("^")
	for _, char := range pattern {
		switch char {
		case '*':
			sb.WriteString(".*")
		case '?', '.', '+', '(', ')', '[', ']', '{', '}', '\\', '^', '|':
			sb.WriteByte('\\')
			sb.WriteRune(char)
		default:
			sb.WriteRune(char)
		}
	}
	if anchored {
		sb.WriteString("$")
	}
	compiled, err := regexp.Compile(sb.String())
	if err != nil {
		// Unreachable for well-formed builders, but never panic: a broken
		// pattern simply never matches.
		return regexp.MustCompile(`^\z.\z`)
	}
	return compiled
}

// newRobotsRule compiles one pattern, returning ok=false for an empty one.
func newRobotsRule(pattern string) (robotsRule, bool) {
	anchored := strings.HasSuffix(pattern, "$")
	length := len(pattern)
	if anchored {
		length--
	}
	if length <= 0 {
		return robotsRule{}, false
	}
	return robotsRule{
		raw:    pattern,
		re:     robotsPatternToRegexp(pattern),
		length: length,
	}, true
}

// matches reports whether urlPath (path + query, per robots.txt spec) matches.
func (rule robotsRule) matches(urlPath string) bool {
	return rule.re.MatchString(urlPath)
}

// Allow reports whether urlPath (path + query) may be fetched. Among matching
// Allow/Disallow patterns the longest wins; on equal length Allow wins
// (Google robots.txt matching spec).
func (rules *RobotsRules) Allow(urlPath string) bool {
	if rules == nil {
		return true
	}
	bestLength := -1
	bestAllows := true
	check := func(group []robotsRule, allows bool) {
		for _, rule := range group {
			if rule.length > bestLength && rule.matches(urlPath) {
				bestLength = rule.length
				bestAllows = allows
			}
		}
	}
	// Check allow first so a tie resolves to Allow without a separate branch.
	check(rules.allow, true)
	check(rules.disallow, false)
	return bestAllows
}

// ParseRobotsTxt parses the robots.txt format. Only rules in the `*`
// user-agent group apply; other groups, comments, and unknown tokens are
// ignored. An empty Disallow value means allow-all for that pattern.
func ParseRobotsTxt(body []byte) *RobotsRules {
	rules := &RobotsRules{}
	inStarGroup := false
	groupFinished := false

	for _, line := range strings.Split(string(body), "\n") {
		// Strip comments (# may start mid-line) and surrounding whitespace.
		if hashIndex := strings.IndexByte(line, '#'); hashIndex >= 0 {
			line = line[:hashIndex]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		colonIndex := strings.IndexByte(line, ':')
		if colonIndex < 0 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(line[:colonIndex]))
		value := strings.TrimSpace(line[colonIndex+1:])

		switch field {
		case "user-agent":
			// A user-agent line after rules starts a new group.
			if groupFinished || !inStarGroup {
				inStarGroup = value == "*"
				groupFinished = false
			}
		case "disallow", "allow", "crawl-delay":
			if !inStarGroup {
				continue
			}
			groupFinished = true
			switch field {
			case "disallow":
				// Empty value means allow-all: no rule to record.
				if rule, ok := newRobotsRule(value); ok {
					rules.disallow = append(rules.disallow, rule)
				}
			case "allow":
				if rule, ok := newRobotsRule(value); ok {
					rules.allow = append(rules.allow, rule)
				}
			case "crawl-delay":
				if seconds, err := time.ParseDuration(value + "s"); err == nil {
					rules.CrawlDelay = seconds
				}
			}
		}
	}
	return rules
}

// FetchRobotsRules fetches and parses <scheme>://<host>/robots.txt once.
// Any failure (network error, non-2xx, empty body) yields allow-all rules, so
// a missing robots.txt never blocks a crawl.
func FetchRobotsRules(ctx context.Context, fetcher *Fetcher, rootURL *url.URL) *RobotsRules {
	if fetcher == nil {
		return nil
	}
	robotsURL := rootURL.Scheme + "://" + rootURL.Host + "/robots.txt"
	result := fetcher.Fetch(ctx, robotsURL)
	if result.FetchError != nil || result.StatusCode < 200 || result.StatusCode >= 300 || len(result.Body) == 0 {
		log.Printf("robots.txt unavailable, allowing all urls: url=%q err=%v status=%d", robotsURL, result.FetchError, result.StatusCode)
		return nil
	}
	return ParseRobotsTxt(result.Body)
}
