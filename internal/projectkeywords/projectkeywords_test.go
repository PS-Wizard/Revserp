package projectkeywords

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeProjectKeywordDisplay(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"trims", "  plumber ", "plumber"},
		{"collapses inner whitespace", "emergency   plumber\tnear\nme", "emergency plumber near me"},
		{"preserves casing", "ACME Shoes", "ACME Shoes"},
		{"blank", "   ", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeProjectKeywordDisplay(tc.input); got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeProjectKeywordKey(t *testing.T) {
	if got := NormalizeProjectKeywordKey("  ACME   Shoes "); got != "acme shoes" {
		t.Fatalf("key = %q, want %q", got, "acme shoes")
	}
}

func TestValidateProjectKeywordPhrase(t *testing.T) {
	if _, err := ValidateProjectKeywordPhrase("   "); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("blank err = %v, want invalid", err)
	}
	long := strings.Repeat("a", MaxProjectKeywordRunes+1)
	if _, err := ValidateProjectKeywordPhrase(long); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("long err = %v, want invalid", err)
	}
	ok := strings.Repeat("b", MaxProjectKeywordRunes)
	display, err := ValidateProjectKeywordPhrase("  " + ok + "  ")
	if err != nil {
		t.Fatalf("limit-length err = %v", err)
	}
	if display != ok {
		t.Fatalf("display = %q, want trimmed %q", display, ok)
	}
}

func TestValidateProjectKeywordKind(t *testing.T) {
	for _, kind := range []string{ProjectKeywordKindBrand, ProjectKeywordKindNonBrand} {
		if err := ValidateProjectKeywordKind(kind); err != nil {
			t.Fatalf("kind %q err = %v", kind, err)
		}
	}
	if err := ValidateProjectKeywordKind("target"); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("kind target err = %v, want invalid", err)
	}
}

func TestBuildProjectKeywordListsUserWins(t *testing.T) {
	rows := []StoredProjectKeyword{
		{ID: "r1", Keyword: "revserp shoes", Normalized: "revserp shoes", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceRevserp},
		{ID: "u1", Keyword: "ACME", Normalized: "acme", Kind: ProjectKeywordKindBrand, Source: ProjectKeywordSourceUser},
		{ID: "r2", Keyword: "acme", Normalized: "acme", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceRevserp},
		{ID: "u2", Keyword: "trail boots", Normalized: "trail boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceUser},
	}
	lists := BuildProjectKeywordLists(rows)
	if len(lists.UserDefined) != 2 || len(lists.RevserpSuggested) != 2 {
		t.Fatalf("sources = %d/%d, want 2/2", len(lists.UserDefined), len(lists.RevserpSuggested))
	}
	if len(lists.Combined) != 3 {
		t.Fatalf("combined = %#v, want 3 entries", lists.Combined)
	}
	first := lists.Combined[0]
	if first.Keyword != "ACME" || first.Kind != ProjectKeywordKindBrand {
		t.Fatalf("first combined = %#v, want user ACME/brand", first)
	}
	if len(first.Sources) != 2 || first.Sources[0] != ProjectKeywordSourceUser || first.Sources[1] != ProjectKeywordSourceRevserp {
		t.Fatalf("first sources = %v, want [user revserp]", first.Sources)
	}
	if got := CombinedKeywordTexts(lists); strings.Join(got, "|") != "ACME|trail boots|revserp shoes" {
		t.Fatalf("texts = %v", got)
	}
}

func TestBuildProjectKeywordListsEmptyNonNull(t *testing.T) {
	lists := BuildProjectKeywordLists(nil)
	if lists.UserDefined == nil || lists.RevserpSuggested == nil || lists.Combined == nil {
		t.Fatalf("lists = %#v, want non-nil slices", lists)
	}
	if texts := CombinedKeywordTexts(lists); len(texts) != 0 {
		t.Fatalf("texts = %v, want []", texts)
	}
}

func TestNormalizeSuggestedProjectKeywords(t *testing.T) {
	brand, nonBrand, err := NormalizeSuggestedProjectKeywords(
		[]string{"ACME", "acme boots", "  "},
		[]string{"trail boots", "ACME", "trail  boots"},
	)
	if err != nil {
		t.Fatalf("normalize err = %v", err)
	}
	if strings.Join(nonBrand, "|") != "trail boots|ACME" {
		t.Fatalf("nonBrand = %v", nonBrand)
	}
	if strings.Join(brand, "|") != "acme boots" {
		t.Fatalf("brand = %v, want overlap dropped to non-brand", brand)
	}
}

func TestNormalizeSuggestedProjectKeywordsRequiresBoth(t *testing.T) {
	if _, _, err := NormalizeSuggestedProjectKeywords([]string{"ACME"}, nil); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("empty non-brand err = %v, want invalid", err)
	}
	if _, _, err := NormalizeSuggestedProjectKeywords(nil, []string{"boots"}); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("empty brand err = %v, want invalid", err)
	}
	if _, _, err := NormalizeSuggestedProjectKeywords([]string{"boots"}, []string{"boots"}); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("full-overlap err = %v, want invalid", err)
	}
}

func TestNormalizeSuggestedProjectKeywordsRejectsExcessWithoutTruncation(t *testing.T) {
	if MaxProjectKeywordsPerKindPerSource != 10 {
		t.Fatalf("cap = %d, want 10", MaxProjectKeywordsPerKindPerSource)
	}
	makeList := func(n int, prefix string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + " keyword " + strings.Repeat("x", i+1)
		}
		return out
	}
	if _, _, err := NormalizeSuggestedProjectKeywords(makeList(10, "brand"), makeList(10, "need")); err != nil {
		t.Fatalf("10+10 err = %v, want accepted", err)
	}
	if _, _, err := NormalizeSuggestedProjectKeywords(makeList(11, "brand"), []string{"one"}); !errors.Is(err, ErrProjectKeywordLimit) {
		t.Fatalf("11-brand err = %v, want keyword limit", err)
	}
	if _, _, err := NormalizeSuggestedProjectKeywords([]string{"one"}, makeList(11, "need")); !errors.Is(err, ErrProjectKeywordLimit) {
		t.Fatalf("11-non-brand err = %v, want keyword limit", err)
	}
}

func TestNormalizeSuggestedProjectKeywordsRejectsInvalidPhrase(t *testing.T) {
	for _, invalid := range []string{strings.Repeat("ü", MaxProjectKeywordRunes+1), "bad\x00keyword", string([]byte{0xff})} {
		if _, _, err := NormalizeSuggestedProjectKeywords([]string{"brand", invalid}, []string{"service"}); !errors.Is(err, ErrProjectKeywordInvalid) {
			t.Fatalf("invalid phrase error = %v, want invalid keyword", err)
		}
	}
}

func TestProjectKeywordNormalizationContract(t *testing.T) {
	cases := []struct{ input, display, key string }{
		{"  Emergency   Plumber ", "Emergency Plumber", "emergency plumber"},
		{"a\tb\nc\rd", "a b c d", "a b c d"},
		{"ACME Shoes", "ACME Shoes", "acme shoes"},
		{"café  MÜNCHEN", "café MÜNCHEN", "café münchen"},
		{"   ", "", ""},
	}
	for _, tc := range cases {
		if got := NormalizeProjectKeywordDisplay(tc.input); got != tc.display {
			t.Errorf("display(%q) = %q, want %q", tc.input, got, tc.display)
		}
		if got := NormalizeProjectKeywordKey(tc.input); got != tc.key {
			t.Errorf("key(%q) = %q, want %q", tc.input, got, tc.key)
		}
	}
}

func TestOverlongPhraseRejectedForNewWrites(t *testing.T) {
	long := strings.Repeat("ü", MaxProjectKeywordRunes+1)
	if _, err := ValidateProjectKeywordPhrase(long); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("overlong err = %v, want invalid", err)
	}
}

func TestOverlongLegacyRowsStayReadable(t *testing.T) {
	long := strings.Repeat("w", MaxProjectKeywordRunes+50)
	lists := BuildProjectKeywordLists([]StoredProjectKeyword{
		{ID: "legacy-long", Keyword: long, Normalized: strings.ToLower(long), Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceUser},
	})
	if len(lists.Combined) != 1 || lists.Combined[0].Keyword != long {
		t.Fatalf("combined = %#v, want verbatim overlong entry", lists.Combined)
	}
	if texts := CombinedKeywordTexts(lists); len(texts) != 1 || texts[0] != long {
		t.Fatalf("texts length = %d, want 1 verbatim entry", len(texts))
	}
	if len(lists.UserDefined) != 1 || lists.UserDefined[0].ID != "legacy-long" {
		t.Fatalf("user_defined = %#v, want deletable row", lists.UserDefined)
	}
}

func TestBuildProjectKeywordListsMergesUnicodeWhitespaceVariants(t *testing.T) {
	rows := []StoredProjectKeyword{
		{ID: "r1", Keyword: "trail\u2003boots", Normalized: "trail\u2003boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceRevserp},
		{ID: "u1", Keyword: "trail boots", Normalized: "trail boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceUser},
		{ID: "r2", Keyword: "acme", Normalized: "acme", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceRevserp},
		{ID: "u2", Keyword: "ACME", Normalized: "ACME", Kind: ProjectKeywordKindBrand, Source: ProjectKeywordSourceUser},
	}
	lists := BuildProjectKeywordLists(rows)
	if len(lists.Combined) != 2 {
		t.Fatalf("combined = %#v, want 2 merged entries", lists.Combined)
	}
	first := lists.Combined[0]
	if first.Keyword != "trail boots" || first.Kind != ProjectKeywordKindNonBrand {
		t.Fatalf("first = %#v, want user trail boots/non_brand", first)
	}
	if len(first.Sources) != 2 {
		t.Fatalf("first sources = %v, want both badges", first.Sources)
	}
	second := lists.Combined[1]
	if second.Keyword != "ACME" || second.Kind != ProjectKeywordKindBrand {
		t.Fatalf("second = %#v, want user ACME/brand", second)
	}
	if len(second.Sources) != 2 {
		t.Fatalf("second sources = %v, want both badges", second.Sources)
	}
}
