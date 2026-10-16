package app

import (
	"context"

	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestParseComparisonExcludedURLPatterns(t *testing.T) {
	for _, raw := range []string{"", "   ", "null", "{}"} {
		patterns, err := parseComparisonExcludedURLPatterns([]byte(raw))
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if len(patterns) != 0 {
			t.Errorf("parse %q = %v, want no patterns", raw, patterns)
		}
	}
	patterns, err := parseComparisonExcludedURLPatterns([]byte(`{"max_depth":3,"excluded_url_patterns":["/blogs/*"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 1 || patterns[0] != "/blogs/*" {
		t.Errorf("patterns = %v, want [/blogs/*]", patterns)
	}
	if _, err := parseComparisonExcludedURLPatterns([]byte(`{broken`)); err == nil {
		t.Errorf("malformed snapshot should error honestly")
	}
}

func mustComparisonExclusionMatcher(t *testing.T, patterns []string) *crawler.ExcludedURLMatcher {
	t.Helper()
	matcher, err := crawler.CompileExcludedURLPatterns(patterns)
	if err != nil {
		t.Fatalf("compile %v: %v", patterns, err)
	}
	if matcher == nil {
		t.Fatalf("compile %v returned nil matcher", patterns)
	}
	return matcher
}

func TestCountComparisonExcludedIssueTypesBareDirectoryWildcard(t *testing.T) {
	matcher := mustComparisonExclusionMatcher(t, []string{"/blogs/*"})
	candidates := []sqlc.ListComparisonExcludedCandidateURLsRow{
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs/category/paid-media/"},
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs/page/2/"},
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs/page/2/"},
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs"},
		{Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Url: "https://example.com/about"},
	}
	excluded := countComparisonExcludedIssueTypes(candidates, matcher.Excludes)
	if len(excluded) != 1 {
		t.Fatalf("excluded = %+v, want one entry", excluded)
	}
	got := excluded[0]
	if got.Pillar != "seo" || got.Bucket != "internal_linking" || got.IssueType != "low_internal_links_in" || got.ExcludedURLCount != 3 {
		t.Errorf("excluded = %+v, want seo/internal_linking/low_internal_links_in count 3", got)
	}
}

func TestCountComparisonExcludedIssueTypesFullURLQueryAndLiterals(t *testing.T) {
	matcher := mustComparisonExclusionMatcher(t, []string{
		"https://example.com/search?query=foo*",
		"/price-100$-exact",
	})
	candidates := []sqlc.ListComparisonExcludedCandidateURLsRow{
		{Pillar: "seo", Bucket: "technical_seo", IssueType: "client_error_status", Url: "https://example.com/search?query=foobar"},
		{Pillar: "seo", Bucket: "technical_seo", IssueType: "client_error_status", Url: "https://example.com/price-100$-exact"},
		{Pillar: "seo", Bucket: "technical_seo", IssueType: "client_error_status", Url: "https://example.com/price-100X-exact"},
	}
	excluded := countComparisonExcludedIssueTypes(candidates, matcher.Excludes)
	if len(excluded) != 1 {
		t.Fatalf("excluded = %+v, want one entry", excluded)
	}
	if excluded[0].ExcludedURLCount != 2 {
		t.Errorf("excluded count = %d, want 2", excluded[0].ExcludedURLCount)
	}
}

func TestCountComparisonExcludedIssueTypesStableSorted(t *testing.T) {
	matcher := mustComparisonExclusionMatcher(t, []string{"/*"})
	candidates := []sqlc.ListComparisonExcludedCandidateURLsRow{
		{Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Url: "https://example.com/b"},
		{Pillar: "aeo", Bucket: "answerability", IssueType: "missing_website_schema", Url: "https://example.com/a"},
		{Pillar: "seo", Bucket: "content_structure", IssueType: "missing_h1", Url: "https://example.com/c"},
	}
	excluded := countComparisonExcludedIssueTypes(candidates, matcher.Excludes)
	want := []comparisonExcludedIssueType{
		{Pillar: "aeo", Bucket: "answerability", IssueType: "missing_website_schema", ExcludedURLCount: 1},
		{Pillar: "seo", Bucket: "content_structure", IssueType: "missing_h1", ExcludedURLCount: 1},
		{Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", ExcludedURLCount: 1},
	}
	if len(excluded) != len(want) {
		t.Fatalf("excluded = %+v, want %+v", excluded, want)
	}
	for i := range want {
		if excluded[i] != want[i] {
			t.Errorf("excluded[%d] = %+v, want %+v", i, excluded[i], want[i])
		}
	}
}

func TestLoadComparisonExcludedIssueTypesShortCircuitsWithoutQuery(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		excluded, err := loadComparisonExcludedIssueTypes(context.Background(), nil, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, []byte(raw))
		if err != nil {
			t.Fatalf("load %q: %v", raw, err)
		}
		if excluded != nil {
			t.Errorf("load %q = %+v, want nil so the field stays omitted", raw, excluded)
		}
	}
}

func TestFilterComparisonExcludedURLsMatchesRealMatcherAndDedupes(t *testing.T) {
	matcher := mustComparisonExclusionMatcher(t, []string{"/blogs/*"})
	candidates := []sqlc.ListComparisonExcludedCandidateURLsRow{
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs/page/2/"},
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs/page/2/"},
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs/category/paid-media/"},
		{Pillar: "seo", Bucket: "internal_linking", IssueType: "low_internal_links_in", Url: "https://example.com/blogs"},
		{Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Url: "https://example.com/about"},
		{Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Url: "://bad"},
	}
	got := filterComparisonExcludedURLs(candidates, matcher.Excludes)
	want := []string{"https://example.com/blogs", "https://example.com/blogs/category/paid-media/", "https://example.com/blogs/page/2/"}
	if len(got) != len(want) {
		t.Fatalf("excluded = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("excluded[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadComparisonExcludedURLsShortCircuitsWithoutQuery(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		excluded, err := loadComparisonExcludedURLs(context.Background(), nil, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, "seo", "internal_linking", "low_internal_links_in", []byte(raw))
		if err != nil {
			t.Fatalf("load %q: %v", raw, err)
		}
		if excluded != nil {
			t.Errorf("load %q = %+v, want nil so no URLs join the union", raw, excluded)
		}
	}
}
