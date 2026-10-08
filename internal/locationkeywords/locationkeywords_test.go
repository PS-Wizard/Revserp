package locationkeywords

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

func TestNormalizeKeywordGroupKeepsExplicitLists(t *testing.T) {
	brand, nonBrand, err := NormalizeKeywordGroup(
		[]string{" ACME ", "", "acme"},
		[]string{"trail  boots", "Trail Boots", "  "},
	)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(brand) != 1 || brand[0] != "ACME" {
		t.Fatalf("brand = %#v", brand)
	}
	if len(nonBrand) != 1 || nonBrand[0] != "trail boots" {
		t.Fatalf("nonBrand = %#v", nonBrand)
	}
}

func TestNormalizeKeywordGroupConflict(t *testing.T) {
	if _, _, err := NormalizeKeywordGroup([]string{"Acme"}, []string{"ACME"}); !errors.Is(err, ErrKeywordConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
}

func TestNormalizeKeywordGroupNoCountCap(t *testing.T) {
	if _, _, err := NormalizeKeywordGroup([]string{"   "}, nil); err != nil {
		t.Fatalf("blank-only err = %v, want nil", err)
	}
	long := strings.Repeat("a", projectkeywords.MaxProjectKeywordRunes+1)
	if _, _, err := NormalizeKeywordGroup([]string{long}, nil); !errors.Is(err, projectkeywords.ErrProjectKeywordInvalid) {
		t.Fatalf("overlong err = %v, want invalid", err)
	}
	many := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		many = append(many, fmt.Sprintf("phrase %d", i))
	}
	brand, _, err := NormalizeKeywordGroup(many, nil)
	if err != nil {
		t.Fatalf("long explicit list err = %v, want nil: location lists are uncapped", err)
	}
	if len(brand) != 60 {
		t.Fatalf("kept %d phrases, want 60", len(brand))
	}
}

func TestBuildSuggestedKeywords(t *testing.T) {
	group := BuildSuggestedKeywords(
		[]string{"Plumber", "plumber", "Drain Cleaning"},
		[]string{"Kamalpokhari", "Kathmandu", "plumber"},
		[]string{"Durbar Marg", "kathmandu"},
		"Acme Plumbing",
	)
	if len(group.Branded) != 0 {
		t.Fatalf("branded = %#v, suggestions carry no brand phrase here", group.Branded)
	}
	for _, want := range []string{
		"Plumber", "Drain Cleaning", "Kamalpokhari", "Kathmandu", "Durbar Marg",
		"Plumber in Kathmandu", "Plumber in Kamalpokhari", "Plumber in Durbar Marg",
		"Drain Cleaning in Kathmandu",
	} {
		found := false
		for _, got := range group.NonBranded {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("suggestions %#v lack %q", group.NonBranded, want)
		}
	}
	if !sort.StringsAreSorted(group.NonBranded) {
		t.Fatalf("suggestions are not deterministic: %#v", group.NonBranded)
	}
	seen := map[string]bool{}
	for _, phrase := range group.NonBranded {
		if seen[phrase] {
			t.Fatalf("duplicate suggestion %q", phrase)
		}
		seen[phrase] = true
	}
	branded := BuildSuggestedKeywords(nil, nil, nil, "Acme")
	if len(branded.Branded) != 0 || len(branded.NonBranded) != 0 {
		t.Fatalf("empty inputs must stay empty: %#v", branded)
	}
	exact := BuildSuggestedKeywords([]string{"Acme"}, nil, nil, "acme  ")
	if len(exact.Branded) != 1 || len(exact.NonBranded) != 0 {
		t.Fatalf("brand-matching suggestion must classify branded: %#v", exact)
	}
}

func TestOriginForKey(t *testing.T) {
	keys := SuggestionKeys{
		Services:   map[string]struct{}{"plumber": {}},
		Localities: map[string]struct{}{"kathmandu": {}},
		Landmarks:  map[string]pgtype.UUID{"durbar marg": {Bytes: [16]byte{9}, Valid: true}},
	}
	cases := map[string]string{
		"plumber": "service", "kathmandu": "locality", "durbar marg": "landmark", "other": "service",
	}
	for key, want := range cases {
		if got := OriginForKey(key, keys); got != want {
			t.Fatalf("origin(%q) = %q, want %q", key, got, want)
		}
	}

	if got := LandmarkIDForKey("durbar marg", keys); !got.Valid {
		t.Fatal("exact landmark match must resolve an ID")
	}
	if got := LandmarkIDForKey("durbar", keys); got.Valid {
		t.Fatal("partial landmark match must not resolve an ID")
	}
}

func TestGroupStoredKeywords(t *testing.T) {
	rows := []StoredKeyword{
		{Keyword: "Acme", Kind: "brand", Source: "user"},
		{Keyword: "Plumber", Kind: "non_brand", Source: "selected"},
		{Keyword: "Drain", Kind: "non_brand", Source: "user"},
	}
	user := GroupStoredKeywords(rows, "user")
	if len(user.Branded) != 1 || len(user.NonBranded) != 1 {
		t.Fatalf("user = %#v", user)
	}
	selected := SelectedKeywordTexts(rows)
	if len(selected) != 1 || selected[0] != "Plumber" {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestSuggestedSourcesForPhrase(t *testing.T) {
	keys := SuggestionKeys{
		Services:   map[string]struct{}{"plumber": {}},
		Localities: map[string]struct{}{"kathmandu": {}},
		Landmarks:  map[string]pgtype.UUID{"durbar marg": {Bytes: [16]byte{9}, Valid: true}},
	}
	cases := map[string][]string{
		"Plumber":                {"service"},
		"kathmandu":              {"locality"},
		"Durbar Marg":            {"landmark"},
		"Plumber in Kathmandu":   {"service", "locality"},
		"Plumber in Durbar Marg": {"service", "landmark"},
		"Unknown":                nil,
		"Kathmandu in Plumber":   nil,
		"":                       nil,
	}
	for phrase, want := range cases {
		got := SuggestedSourcesForPhrase(phrase, keys)
		if len(got) != len(want) {
			t.Fatalf("sources(%q) = %#v, want %#v", phrase, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("sources(%q) = %#v, want %#v", phrase, got, want)
			}
		}
	}
}

func TestSuggestedOriginsForGroup(t *testing.T) {
	keys := SuggestionKeys{
		Services:   map[string]struct{}{"plumber": {}},
		Localities: map[string]struct{}{"kathmandu": {}},
		Landmarks:  map[string]pgtype.UUID{},
	}
	group := KeywordGroup{
		Branded:    []string{"Acme"},
		NonBranded: []string{"Plumber", "Plumber in Kathmandu", "Unknown"},
	}
	origins := SuggestedOriginsForGroup(group, keys)
	if len(origins) != 2 {
		t.Fatalf("origins = %#v", origins)
	}
	if got := origins["plumber"]; len(got) != 1 || got[0] != "service" {
		t.Fatalf("plumber origin = %#v", got)
	}
	if got := origins["plumber in kathmandu"]; len(got) != 2 || got[0] != "service" || got[1] != "locality" {
		t.Fatalf("combination origin = %#v", got)
	}
}
