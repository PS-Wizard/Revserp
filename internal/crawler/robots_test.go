package crawler

import (
	"testing"
)

func TestParseRobotsTxtAllow(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		path     string
		expected bool
	}{
		{
			name:     "empty body allows all",
			body:     "",
			path:     "/private/secret",
			expected: true,
		},
		{
			name:     "garbage body allows all",
			body:     "this is not robots.txt\r\njust some random text",
			path:     "/anything",
			expected: true,
		},
		{
			name:     "empty disallow value allows all",
			body:     "User-agent: *\nDisallow:",
			path:     "/private/secret",
			expected: true,
		},
		{
			name:     "disallow all via slash",
			body:     "User-agent: *\nDisallow: /",
			path:     "/private/secret",
			expected: false,
		},
		{
			name:     "prefix match blocks deeper path",
			body:     "User-agent: *\nDisallow: /private\nAllow: /",
			path:     "/private/secret",
			expected: false,
		},
		{
			name:     "unmatched path allowed",
			body:     "User-agent: *\nDisallow: /private\n",
			path:     "/public/page",
			expected: true,
		},
		{
			name:     "longest match disallow wins",
			body:     "User-agent: *\nDisallow: /private/secret\nAllow: /private\n",
			path:     "/private/secret/page",
			expected: false,
		},
		{
			name:     "longest match allow wins",
			body:     "User-agent: *\nDisallow: /private\nAllow: /private/public/page\n",
			path:     "/private/public/page",
			expected: true,
		},
		{
			name:     "tie resolves to allow",
			body:     "User-agent: *\nDisallow: /foo\nAllow: /foo\n",
			path:     "/foo/bar",
			expected: true,
		},
		{
			name:     "star wildcard in pattern",
			body:     "User-agent: *\nDisallow: /*.pdf$\n",
			path:     "/docs/report.pdf",
			expected: false,
		},
		{
			name:     "star wildcard non-match",
			body:     "User-agent: *\nDisallow: /*.pdf$\n",
			path:     "/docs/report.pdf.html",
			expected: true,
		},
		{
			name:     "dollar anchor end of path",
			body:     "User-agent: *\nDisallow: /private$\n",
			path:     "/private",
			expected: false,
		},
		{
			name:     "dollar anchor does not match deeper path",
			body:     "User-agent: *\nDisallow: /private$\n",
			path:     "/private/secret",
			expected: true,
		},
		{
			name:     "other user-agent groups ignored",
			body:     "User-agent: Googlebot\nDisallow: /\n\nUser-agent: *\nDisallow: /private\n",
			path:     "/public/page",
			expected: true,
		},
		{
			name:     "comments and unknown tokens ignored",
			body:     "# site robots\nUser-agent: * # star\nDisallow: /private # comment\nNoindex: /junk\nSitemap: https://example.com/sitemap.xml\n",
			path:     "/private/x",
			expected: false,
		},
		{
			name:     "rules apply across consecutive user-agent lines",
			body:     "User-agent: *\nUser-agent: Otherbot\nDisallow: /private\n",
			path:     "/private/x",
			expected: false,
		},
		{
			name:     "query string participates in matching",
			body:     "User-agent: *\nDisallow: /*?sort=\n",
			path:     "/products?sort=asc",
			expected: false,
		},
		{
			name:     "lowercase fields, case-sensitive paths",
			body:     "user-agent: *\ndisallow: /private\nallow: /\n",
			path:     "/private/x",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := ParseRobotsTxt([]byte(tt.body))
			if got := rules.Allow(tt.path); got != tt.expected {
				t.Errorf("Allow(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

func TestParseRobotsTxtCrawlDelay(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		expectedDuration float64 // seconds; -1 means "not set"
	}{
		{name: "crawl delay parsed", body: "User-agent: *\nCrawl-delay: 5\n", expectedDuration: 5},
		{name: "crawl delay missing", body: "User-agent: *\nDisallow: /\n", expectedDuration: -1},
		{name: "crawl delay garbage ignored", body: "User-agent: *\nCrawl-delay: soon\n", expectedDuration: -1},
		{name: "other group crawl delay ignored", body: "User-agent: Googlebot\nCrawl-delay: 9\n\nUser-agent: *\n", expectedDuration: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := ParseRobotsTxt([]byte(tt.body))
			if tt.expectedDuration < 0 {
				if rules.CrawlDelay != 0 {
					t.Errorf("CrawlDelay = %v, want unset", rules.CrawlDelay)
				}
				return
			}
			if rules.CrawlDelay.Seconds() != tt.expectedDuration {
				t.Errorf("CrawlDelay = %v, want %v seconds", rules.CrawlDelay, tt.expectedDuration)
			}
		})
	}
}

func TestNilRobotsRulesAllowAll(t *testing.T) {
	var rules *RobotsRules
	if !rules.Allow("/anything") {
		t.Error("nil RobotsRules must allow all")
	}
}

func TestNormalizeConfigSnapshotHonourRobotsTxtDefault(t *testing.T) {
	t.Run("defaults to false when absent", func(t *testing.T) {
		snapshot, _, err := NormalizeConfigSnapshot([]byte(`{}`))
		if err != nil {
			t.Fatalf("NormalizeConfigSnapshot: %v", err)
		}
		if snapshot.HonourRobotsTxt {
			t.Error("HonourRobotsTxt must default to false")
		}
	})

	t.Run("defaults to false for empty snapshot", func(t *testing.T) {
		snapshot, _, err := NormalizeConfigSnapshot(nil)
		if err != nil {
			t.Fatalf("NormalizeConfigSnapshot: %v", err)
		}
		if snapshot.HonourRobotsTxt {
			t.Error("HonourRobotsTxt must default to false for empty snapshot")
		}
	})

	t.Run("honours explicit true", func(t *testing.T) {
		snapshot, _, err := NormalizeConfigSnapshot([]byte(`{"honour_robots_txt":true}`))
		if err != nil {
			t.Fatalf("NormalizeConfigSnapshot: %v", err)
		}
		if !snapshot.HonourRobotsTxt {
			t.Error("HonourRobotsTxt must be true when set")
		}
	})

	t.Run("honours explicit false", func(t *testing.T) {
		snapshot, _, err := NormalizeConfigSnapshot([]byte(`{"honour_robots_txt":false}`))
		if err != nil {
			t.Fatalf("NormalizeConfigSnapshot: %v", err)
		}
		if snapshot.HonourRobotsTxt {
			t.Error("HonourRobotsTxt must be false when explicitly false")
		}
	})
}
