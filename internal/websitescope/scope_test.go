package websitescope

import "testing"

func TestCanonicalizeWebsiteScopeURL(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  string
		isErr bool
	}{
		{name: "root path", raw: "https://branch.example/", want: "https://branch.example/"},
		{name: "strips query and fragment", raw: "https://branch.example/menu?utm=x#top", want: "https://branch.example/menu"},
		{name: "lowercases scheme and host", raw: "HTTPS://Branch.Example/Menu/", want: "https://branch.example/Menu"},
		{name: "drops default port", raw: "https://branch.example:443/menu", want: "https://branch.example/menu"},
		{name: "keeps odd port", raw: "https://branch.example:8443/menu", want: "https://branch.example:8443/menu"},
		{name: "empty path becomes root", raw: "https://branch.example", want: "https://branch.example/"},
		{name: "relative rejected", raw: "/menu", isErr: true},
		{name: "non http rejected", raw: "ftp://branch.example/menu", isErr: true},
		{name: "missing host rejected", raw: "https:///menu", isErr: true},
		{name: "encoded slash rejected", raw: "https://branch.example/a%2Fb", isErr: true},
		{name: "encoded backslash rejected", raw: "https://branch.example/a%5Cb", isErr: true},
		{name: "dot segment rejected", raw: "https://branch.example/a/../b", isErr: true},
		{name: "empty rejected", raw: "  ", isErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalizeWebsiteScopeURL(tc.raw)
			if tc.isErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestMatchWebsiteScopePage(t *testing.T) {
	tests := []struct {
		name  string
		match string
		scope string
		page  string
		want  bool
	}{
		{name: "none never matches", match: "none", scope: "https://branch.example/", page: "https://branch.example/", want: false},
		{name: "exact page", match: "exact", scope: "https://branch.example/menu", page: "https://branch.example/menu?x=1#y", want: true},
		{name: "exact ignores stored trailing slash", match: "exact", scope: "https://branch.example/about", page: "https://branch.example/about/", want: true},
		{name: "exact ignores scope trailing slash", match: "exact", scope: "https://branch.example/about/", page: "https://branch.example/about", want: true},
		{name: "exact root with and without slash", match: "exact", scope: "https://branch.example", page: "https://branch.example/", want: true},
		{name: "subtree self with trailing slash", match: "subtree", scope: "https://branch.example/menu/", page: "https://branch.example/menu/", want: true},
		{name: "subtree child of slashless scope", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu/dinner/", want: true},
		{name: "exact rejects child", match: "exact", scope: "https://branch.example/menu", page: "https://branch.example/menu/dinner", want: false},
		{name: "subtree self", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu", want: true},
		{name: "subtree child", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu/dinner", want: true},
		{name: "subtree segment boundary", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu-special", want: false},
		{name: "subtree root covers all", match: "subtree", scope: "https://branch.example/", page: "https://branch.example/any/page", want: true},
		{name: "foreign origin rejected", match: "subtree", scope: "https://branch.example/", page: "https://other.example/", want: false},
		{name: "scheme differs rejected", match: "exact", scope: "https://branch.example/menu", page: "http://branch.example/menu", want: false},
		{name: "odd port differs rejected", match: "exact", scope: "https://branch.example:8443/menu", page: "https://branch.example/menu", want: false},
		{name: "unknown match rejected", match: "regex", scope: "https://branch.example/", page: "https://branch.example/", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchWebsiteScopePage(tc.match, tc.scope, tc.page); got != tc.want {
				t.Fatalf("match=%q scope=%q page=%q: got %v want %v", tc.match, tc.scope, tc.page, got, tc.want)
			}
		})
	}
}

func TestScopeMatchesParentOrigin(t *testing.T) {
	tests := []struct {
		name   string
		scope  string
		parent string
		want   bool
	}{
		{name: "same origin", scope: "https://branch.example/menu", parent: "https://branch.example", want: true},
		{name: "parent path ignored", scope: "https://branch.example/menu", parent: "https://branch.example/blog", want: true},
		{name: "case and default port fold", scope: "https://branch.example/menu", parent: "HTTPS://Branch.Example:443/", want: true},
		{name: "odd port must match", scope: "https://branch.example:8443/menu", parent: "https://branch.example:8443", want: true},
		{name: "odd port mismatch", scope: "https://branch.example:8443/menu", parent: "https://branch.example", want: false},
		{name: "foreign host", scope: "https://other.example/menu", parent: "https://branch.example", want: false},
		{name: "subdomain is foreign", scope: "https://shop.branch.example/menu", parent: "https://branch.example", want: false},
		{name: "scheme mismatch", scope: "http://branch.example/menu", parent: "https://branch.example", want: false},
		{name: "unparsable parent", scope: "https://branch.example/menu", parent: "://bad", want: false},
		{name: "relative scope", scope: "/menu", parent: "https://branch.example", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopeMatchesParentOrigin(tc.scope, tc.parent); got != tc.want {
				t.Fatalf("scope=%q parent=%q: got %v want %v", tc.scope, tc.parent, got, tc.want)
			}
		})
	}
}

func TestCanonicalizeWebsiteScopeURLRejectsAmbiguousEncodings(t *testing.T) {
	for _, raw := range []string{
		"https://branch.example/a%2fb",
		"https://branch.example/a%2Fb",
		"https://branch.example/a%5cb",
		"https://branch.example/a%5Cb",
		"https://branch.example/a%2Eb",
		"https://branch.example/a%2Eb",
		"https://branch.example/a%252Fb",
		"https://branch.example/a%252Eb",
		"https://branch.example/a%255Cb",
		"https://branch.example/a%255cb",
		"https://branch.example/100%25-off",
		"https://branch.example/./menu",
		"https://branch.example/menu/../admin",
		`https://branch.example/a\b`,
	} {
		if _, err := CanonicalizeWebsiteScopeURL(raw); err == nil {
			t.Fatalf("expected rejection for %q", raw)
		}
	}
}

func TestCanonicalizeWebsiteScopeURLKeepsLegitEscapes(t *testing.T) {
	got, err := CanonicalizeWebsiteScopeURL("https://branch.example/caf%C3%A9/menu?x=%2e#frag")
	if err != nil {
		t.Fatalf("legit UTF-8 escape must pass: %v", err)
	}
	if got != "https://branch.example/caf%C3%A9/menu" {
		t.Fatalf("got %q", got)
	}
}

func TestMatchWebsiteScopePageRejectsAmbiguousPages(t *testing.T) {
	tests := []struct {
		name  string
		match string
		scope string
		page  string
		want  bool
	}{
		{name: "encoded dot traversal never matches subtree", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu/%2e%2e/admin", want: false},
		{name: "mixed case encoded dot never matches", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu/%2E%2E/admin", want: false},
		{name: "double encoded slash never matches", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu%252fadmin", want: false},
		{name: "literal dot segment never matches", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/menu/../admin", want: false},
		{name: "exact rejects encoded form of same page", match: "exact", scope: "https://branch.example/menu", page: "https://branch.example/m%65nu", want: false},
		{name: "legit escaped page still matches", match: "subtree", scope: "https://branch.example/", page: "https://branch.example/caf%C3%A9/menu?x=1", want: true},
		{name: "no dot escapes the branch", match: "subtree", scope: "https://branch.example/menu", page: "https://branch.example/other", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchWebsiteScopePage(tc.match, tc.scope, tc.page); got != tc.want {
				t.Fatalf("match=%q scope=%q page=%q: got %v want %v", tc.match, tc.scope, tc.page, got, tc.want)
			}
		})
	}
}
