package crawler

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustCompileExcluded(t *testing.T, patterns ...string) *ExcludedURLMatcher {
	t.Helper()
	matcher, err := CompileExcludedURLPatterns(patterns)
	if err != nil {
		t.Fatalf("compile excluded patterns %q: %v", patterns, err)
	}
	return matcher
}

func TestNormalizeExcludedURLPatternsValidation(t *testing.T) {
	valid := []struct {
		name  string
		input []string
		want  []string
	}{
		{"none defaults to none", nil, []string{}},
		{"empty entries dropped", []string{"", "   "}, []string{}},
		{"trims and dedupes", []string{" /a ", "/a", "/b/* "}, []string{"/a", "/b/*"}},
		{"full url kept", []string{"https://example.com/private"}, []string{"https://example.com/private"}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeExcludedURLPatterns(test.input)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("got %q, want %q", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("got %q, want %q", got, test.want)
				}
			}
		})
	}

	invalid := []struct {
		name  string
		input string
	}{
		{"ftp protocol", "ftp://example.com/x"},
		{"javascript scheme", "javascript:alert(1)"},
		{"bare host without scheme", "example.com/x"},
		{"relative path without slash", "blog/x"},
		{"protocol relative", "//example.com/x"},
		{"query in path rule", "/search?q=x"},
		{"fragment in path rule", "/page#top"},
		{"fragment in full url", "https://example.com/page#top"},
		{"whitespace inside", "/my page"},
		{"control character", "/a\x7fb"},
		{"missing host", "https:///x"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeExcludedURLPatterns([]string{test.input}); err == nil {
				t.Fatalf("expected error for %q, got nil", test.input)
			}
		})
	}
}

func TestNormalizeExcludedURLPatternsLimits(t *testing.T) {
	many := make([]string, MaxExcludedURLPatterns+1)
	for i := range many {
		many[i] = "/page-" + strings.Repeat("a", i%10) + string(rune('a'+i%26)) + strings.Repeat("b", i/26)
	}
	if _, err := NormalizeExcludedURLPatterns(many); err == nil {
		t.Fatalf("expected error for %d patterns, got nil", len(many))
	}

	tooLong := "/" + strings.Repeat("a", MaxExcludedURLPatternLength)
	if _, err := NormalizeExcludedURLPatterns([]string{tooLong}); err == nil {
		t.Fatal("expected error for overlong pattern, got nil")
	}
}

func TestExcludedURLMatcherSemantics(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		target   string
		want     bool
	}{
		{"exact full url matches", []string{"https://example.com/blog"}, "https://example.com/blog", true},
		{"exact full url is not a prefix", []string{"https://example.com/blog"}, "https://example.com/blog/post", false},
		{"exact full url ignores no query", []string{"https://example.com/blog"}, "https://example.com/blog?x=1", false},
		{"full url wildcard spans separators", []string{"https://example.com/blog/*"}, "https://example.com/blog/a/b?x=1", true},
		{"full url wildcard needs prefix", []string{"https://example.com/blog/*"}, "https://example.com/other", false},
		{"bare host pattern matches root", []string{"https://example.com"}, "https://example.com/", true},
		{"host match is case insensitive", []string{"https://EXAMPLE.com/Admin"}, "https://example.com/Admin", true},
		{"path match is case sensitive", []string{"/Admin"}, "https://example.com/admin", false},
		{"scheme is part of full match", []string{"https://example.com/*"}, "http://example.com/x", false},
		{"path rule matches any host", []string{"/admin"}, "http://other.test/admin", true},
		{"path rule ignores query", []string{"/admin"}, "https://example.com/admin?x=1", true},
		{"path rule is not a prefix", []string{"/admin"}, "https://example.com/administrator", false},
		{"star slash excludes dir itself", []string{"/blog/*"}, "https://example.com/blog", true},
		{"star slash excludes nested", []string{"/blog/*"}, "https://example.com/blog/a/b", true},
		{"star slash keeps siblings", []string{"/blog/*"}, "https://example.com/blogroll", false},
		{"mid path wildcard", []string{"/a/*/c"}, "https://example.com/a/b/c", true},
		{"mid path wildcard needs suffix", []string{"/a/*/c"}, "https://example.com/a/b/d", false},
		{"target fragment stripped", []string{"/admin"}, "https://example.com/admin#sec", true},
		{"unparseable target never excluded", []string{"/*"}, "://bad", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mustCompileExcluded(t, test.patterns...).Excludes(test.target); got != test.want {
				t.Fatalf("Excludes(%q) with %q = %v, want %v", test.target, test.patterns, got, test.want)
			}
		})
	}
}

func TestExcludedURLMatcherNilExcludesNothing(t *testing.T) {
	var matcher *ExcludedURLMatcher
	if matcher.Excludes("https://example.com/anything") {
		t.Fatal("nil matcher must exclude nothing")
	}
	compiled, err := CompileExcludedURLPatterns(nil)
	if err != nil {
		t.Fatalf("compile nil: %v", err)
	}
	if compiled != nil {
		t.Fatal("empty patterns must compile to a nil matcher")
	}
}

func TestNormalizeConfigSnapshotExcludedURLPatternsRoundTrip(t *testing.T) {
	snapshot, normalized, err := NormalizeConfigSnapshot([]byte(`{"excluded_url_patterns":[" /a ","/a","/b/* "]}`))
	if err != nil {
		t.Fatalf("normalize config snapshot: %v", err)
	}
	if len(snapshot.ExcludedURLPatterns) != 2 || snapshot.ExcludedURLPatterns[0] != "/a" || snapshot.ExcludedURLPatterns[1] != "/b/*" {
		t.Fatalf("got snapshot patterns %q", snapshot.ExcludedURLPatterns)
	}
	var decoded map[string]any
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		t.Fatalf("unmarshal normalized snapshot: %v", err)
	}
	raw, ok := decoded["excluded_url_patterns"].([]any)
	if !ok || len(raw) != 2 || raw[0] != "/a" || raw[1] != "/b/*" {
		t.Fatalf("got persisted patterns %v", decoded["excluded_url_patterns"])
	}

	snapshot, normalized, err = NormalizeConfigSnapshot([]byte(`{}`))
	if err != nil {
		t.Fatalf("normalize config snapshot: %v", err)
	}
	if len(snapshot.ExcludedURLPatterns) != 0 {
		t.Fatalf("got default patterns %q, want none", snapshot.ExcludedURLPatterns)
	}
	var defaultDecoded map[string]any
	if err := json.Unmarshal(normalized, &defaultDecoded); err != nil {
		t.Fatalf("unmarshal normalized snapshot: %v", err)
	}
	if _, exists := defaultDecoded["excluded_url_patterns"]; exists {
		t.Fatal("default snapshot must omit excluded_url_patterns")
	}

	if _, _, err := NormalizeConfigSnapshot([]byte(`{"excluded_url_patterns":["ftp://example.com/x"]}`)); err == nil {
		t.Fatal("expected invalid patterns to fail snapshot normalization")
	}
}

func TestConfigFromBaseURLAndSnapshotCompilesExclusionsOnce(t *testing.T) {
	config, err := ConfigFromBaseURLAndSnapshot("https://example.com", []byte(`{"excluded_url_patterns":["/secret"]}`))
	if err != nil {
		t.Fatalf("build crawler config: %v", err)
	}
	if config.ExcludedURLs == nil {
		t.Fatal("expected compiled matcher, got nil")
	}
	if !config.RootExcluded("https://example.com/secret?x=1") {
		t.Fatal("expected /secret to be excluded")
	}
	if config.RootExcluded("https://example.com/open") {
		t.Fatal("expected /open to be allowed")
	}

	config, err = ConfigFromBaseURLAndSnapshot("https://example.com", nil)
	if err != nil {
		t.Fatalf("build crawler config: %v", err)
	}
	if config.ExcludedURLs != nil {
		t.Fatal("expected nil matcher without patterns")
	}
	if config.RootExcluded("https://example.com/anything") {
		t.Fatal("expected nothing excluded without patterns")
	}
}

func TestExcludedURLMatcherTrailingStarSlashExcludesBareDirectory(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		target   string
		want     bool
	}{
		{"path rule excludes bare dir", []string{"/blog/*"}, "https://example.com/blog", true},
		{"path rule excludes nested", []string{"/blog/*"}, "https://example.com/blog/a", true},
		{"path rule keeps sibling prefix", []string{"/blog/*"}, "https://example.com/blogroll", false},
		{"full url rule excludes bare dir", []string{"https://example.com/admin/*"}, "https://example.com/admin", true},
		{"full url rule excludes slash dir", []string{"https://example.com/admin/*"}, "https://example.com/admin/", true},
		{"full url rule excludes nested", []string{"https://example.com/admin/*"}, "https://example.com/admin/users", true},
		{"full url rule keeps sibling prefix", []string{"https://example.com/admin/*"}, "https://example.com/administrator", false},
		{"wildcard before suffix matches leaf dir", []string{"/a/*/leaf/*"}, "https://example.com/a/b/leaf", true},
		{"wildcard before suffix matches nested", []string{"/a/*/leaf/*"}, "https://example.com/a/b/leaf/c", true},
		{"wildcard before suffix keeps other leaf", []string{"/a/*/leaf/*"}, "https://example.com/a/b/other", false},
		{"full url wildcard before suffix matches leaf dir", []string{"https://example.com/a/*/leaf/*"}, "https://example.com/a/b/leaf", true},
		{"full url wildcard before suffix keeps other leaf", []string{"https://example.com/a/*/leaf/*"}, "https://example.com/a/b/other", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mustCompileExcluded(t, test.patterns...).Excludes(test.target); got != test.want {
				t.Fatalf("Excludes(%q) with %q = %v, want %v", test.target, test.patterns, got, test.want)
			}
		})
	}
}

func TestExcludedURLMatcherDecodesPercentEscapes(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		target   string
		want     bool
	}{
		{"encoded path rule matches encoded target", []string{"/caf%C3%A9/*"}, "https://example.com/caf%C3%A9/x", true},
		{"literal path rule matches encoded target", []string{"/café/*"}, "https://example.com/caf%C3%A9/x", true},
		{"encoded exact path rule matches", []string{"/caf%C3%A9"}, "https://example.com/caf%C3%A9", true},
		{"encoded path rule keeps other path", []string{"/caf%C3%A9/*"}, "https://example.com/cafe/x", false},
		{"encoded full url matches encoded target", []string{"https://example.com/caf%C3%A9/*"}, "https://example.com/caf%C3%A9/x", true},
		{"literal full url matches encoded target", []string{"https://example.com/café/*"}, "https://example.com/caf%C3%A9/x", true},
		{"encoded full url bare dir matches", []string{"https://example.com/caf%C3%A9/*"}, "https://example.com/caf%C3%A9", true},
		{"query still matches raw", []string{"https://example.com/s?q=caf%C3%A9"}, "https://example.com/s?q=caf%C3%A9", true},
		{"query mismatch not excluded", []string{"https://example.com/s?q=a"}, "https://example.com/s?q=b", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mustCompileExcluded(t, test.patterns...).Excludes(test.target); got != test.want {
				t.Fatalf("Excludes(%q) with %q = %v, want %v", test.target, test.patterns, got, test.want)
			}
		})
	}
}
