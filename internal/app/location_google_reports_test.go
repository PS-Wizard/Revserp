package app

import (
	"regexp"
	"testing"

	"github.com/ps-wizard/revserp/internal/websitescope"
)

func TestLocationGoogleScopeFiltersMirrorWebsiteMatcher(t *testing.T) {
	tests := []struct {
		name      string
		scopeURL  string
		match     string
		wantPages []string
		dropPages []string
	}{
		{
			name:     "exact mirrors strict path equality, trailing slash and query ignored",
			scopeURL: "https://example.com/blog",
			match:    websitescope.ScopeMatchExact,
			wantPages: []string{
				"https://example.com/blog",
				"https://example.com/blog?utm_source=x",
				"https://example.com/blog/",
				"https://example.com/blog/?a=b",
			},
			dropPages: []string{
				"https://example.com/blogroll",
				"https://example.com/blog/post",
				"https://example.com/blog/post/",
				"https://example.com/other",
			},
		},
		{
			name:     "subtree keeps segment boundary with query variants",
			scopeURL: "https://example.com/blog",
			match:    websitescope.ScopeMatchSubtree,
			wantPages: []string{
				"https://example.com/blog",
				"https://example.com/blog/",
				"https://example.com/blog/post",
				"https://example.com/blog/post/?x=1",
				"https://example.com/blog?utm=x",
			},
			dropPages: []string{
				"https://example.com/blogroll",
				"https://example.com/other",
				"https://other.com/blog/post",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gscFilter, gaFilter, applicable := locationGoogleScopeFilters(test.scopeURL, test.match)
			if !applicable {
				t.Fatal("scope should be applicable")
			}
			expression, ok := gscFilter.Expression, true
			_ = ok
			pattern := regexp.MustCompile(expression)
			for _, page := range test.wantPages {
				if !pattern.MatchString(page) {
					t.Errorf("GSC expression %q drops %q", expression, page)
				}
				if !websitescope.MatchWebsiteScopePage(test.match, test.scopeURL, page) {
					t.Errorf("website matcher disagrees: should match %q", page)
				}
			}
			for _, page := range test.dropPages {
				if pattern.MatchString(page) {
					t.Errorf("GSC expression %q keeps %q", expression, page)
				}
				if websitescope.MatchWebsiteScopePage(test.match, test.scopeURL, page) {
					t.Errorf("website matcher disagrees: should drop %q", page)
				}
			}
			_ = gaFilter
		})
	}
}

func TestLocationGoogleScopeFiltersRejectWholeSite(t *testing.T) {
	for _, scopeURL := range []string{
		"https://example.com",
		"https://example.com/",
		"",
		"not-a-url",
	} {
		if _, _, applicable := locationGoogleScopeFilters(scopeURL, websitescope.ScopeMatchSubtree); applicable {
			t.Errorf("scope %q must read as whole-site, not applicable", scopeURL)
		}
	}
	if _, _, applicable := locationGoogleScopeFilters("https://example.com/blog", websitescope.ScopeMatchNone); applicable {
		t.Error("match=none must not be applicable")
	}
	if _, _, applicable := locationGoogleScopeFilters("https://example.com/blog", "wildcard"); applicable {
		t.Error("unknown match must not be applicable")
	}
}

func TestLocationGoogleScopeFiltersGAParity(t *testing.T) {
	// GA pagePath carries no query string. Exact folds one trailing slash
	// exactly like normalizeScopeMatchPath, so /about/ is branch data while
	// /aboutus and deeper paths are not. Origin is enforced by collection
	// (a property only sees its own traffic), so only paths are compared.
	tests := []struct {
		name      string
		scopeURL  string
		match     string
		wantPaths []string
		dropPaths []string
	}{
		{
			name:      "exact folds trailing slash",
			scopeURL:  "https://example.com/about",
			match:     websitescope.ScopeMatchExact,
			wantPaths: []string{"/about", "/about/"},
			dropPaths: []string{"/aboutus", "/about/post", "/about/post/", "/other", ""},
		},
		{
			name:      "subtree keeps segment boundary",
			scopeURL:  "https://example.com/about",
			match:     websitescope.ScopeMatchSubtree,
			wantPaths: []string{"/about", "/about/", "/about/post", "/about/post/"},
			dropPaths: []string{"/aboutus", "/other"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, gaFilter, applicable := locationGoogleScopeFilters(test.scopeURL, test.match)
			if !applicable {
				t.Fatal("scope should be applicable")
			}
			// FULL_REGEXP means the whole pagePath must match: emulate by
			// anchoring both ends (Go MatchString alone is partial).
			pattern := regexp.MustCompile("^(?:" + gaFilter.Expression + ")$")
			for _, path := range test.wantPaths {
				if !pattern.MatchString(path) {
					t.Errorf("GA expression %q drops %q", gaFilter.Expression, path)
				}
			}
			for _, path := range test.dropPaths {
				if pattern.MatchString(path) {
					t.Errorf("GA expression %q keeps %q", gaFilter.Expression, path)
				}
			}
		})
	}
}

func TestLocationGoogleScopeFiltersGAExpressions(t *testing.T) {
	_, gaFilter, applicable := locationGoogleScopeFilters("https://example.com/blog/", websitescope.ScopeMatchExact)
	if !applicable || gaFilter.Expression != `^/blog/?$` {
		t.Fatalf("GA exact = %q, want ^/blog/?$", gaFilter.Expression)
	}
	_, gaFilter, applicable = locationGoogleScopeFilters("https://example.com/blog", websitescope.ScopeMatchSubtree)
	if !applicable || gaFilter.Expression != `^/blog(/.*)?$` {
		t.Fatalf("GA subtree = %q, want ^/blog(/.*)?$", gaFilter.Expression)
	}
}

func TestLocationReportCoverage(t *testing.T) {
	if locationReportCoverage(true) != locationReportCoverageBranch {
		t.Fatal("scoped reads must be labeled branch")
	}
	if locationReportCoverage(false) != locationReportCoveragePropertyWide {
		t.Fatal("unfiltered reads must be labeled property-wide")
	}
}
