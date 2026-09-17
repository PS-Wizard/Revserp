package businessprofile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func repeatKeyword(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strings.Repeat("x", 3) + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10))
	}
	return out
}

func TestNormalizeStringListCapsAtMax(t *testing.T) {
	many := make([]string, 60)
	for i := range many {
		many[i] = strings.Repeat("k", 2) + string(rune('a'+i%26)) + string(rune('A'+(i/26)%26)) + string(rune('0'+(i/676)%10))
	}
	got := NormalizeStringList(many, 50)
	if len(got) != 50 {
		t.Fatalf("NormalizeStringList capped = %d, want 50", len(got))
	}
}

func TestNormalizeTargetKeywordsCapsAt50(t *testing.T) {
	many := make([]string, 51)
	for i := range many {
		many[i] = "keyword-" + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10)) + "z"
	}
	got := NormalizeTargetKeywords(many)
	if len(got) != 50 {
		t.Fatalf("NormalizeTargetKeywords len = %d, want 50", len(got))
	}
	raw, _ := json.Marshal(got)
	var back []string
	if err := json.Unmarshal(raw, &back); err != nil || len(back) != 50 {
		t.Fatalf("capped list must survive a JSON round-trip, got %s err %v", string(raw), err)
	}
}

func TestNormalizeBusinessCompetitorsCapsAt20(t *testing.T) {
	many := make([]string, 21)
	for i := range many {
		many[i] = "Competitor " + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10))
	}
	got := NormalizeBusinessCompetitors(many)
	if len(got) != 20 {
		t.Fatalf("NormalizeBusinessCompetitors len = %d, want 20", len(got))
	}
	if got == nil || len(got) == 0 {
		t.Fatal("capped list must be non-nil")
	}
}

func TestNormalizeKeywordListsCrossListDedupe(t *testing.T) {
	branded, nonBranded := NormalizeKeywordLists(
		[]string{"Acme Shoes", "acme boots", " Running "},
		[]string{"running shoes", "ACME SHOES", "trail boots"},
	)
	wantNonBranded := []string{"running shoes", "ACME SHOES", "trail boots"}
	if !reflect.DeepEqual(nonBranded, wantNonBranded) {
		t.Fatalf("nonBranded = %v, want %v", nonBranded, wantNonBranded)
	}
	// "Acme Shoes" (dup of non-branded, case-insensitive) and "Running"
	// (substring is not a dup; only exact lowercase matches count)...
	// " Running " trims to "Running", which is not exactly "running shoes",
	// so it stays.
	wantBranded := []string{"acme boots", "Running"}
	if !reflect.DeepEqual(branded, wantBranded) {
		t.Fatalf("branded = %v, want %v", branded, wantBranded)
	}
	for _, b := range branded {
		for _, nb := range nonBranded {
			if strings.ToLower(b) == strings.ToLower(nb) {
				t.Fatalf("lists not disjoint: %q in both", b)
			}
		}
	}
}

func TestNormalizeKeywordListsExactOverlapStaysNonBranded(t *testing.T) {
	branded, nonBranded := NormalizeKeywordLists([]string{"SEO", "Maps"}, []string{"seo", "Analytics"})
	if len(branded) != 1 || branded[0] != "Maps" {
		t.Fatalf("branded = %v, want [Maps]", branded)
	}
	if len(nonBranded) != 2 {
		t.Fatalf("nonBranded = %v, want both kept", nonBranded)
	}
}

func TestNormalizeKeywordListsEmpty(t *testing.T) {
	branded, nonBranded := NormalizeKeywordLists(nil, nil)
	if branded == nil || len(branded) != 0 {
		t.Fatalf("branded = %v, want empty non-nil", branded)
	}
	if nonBranded == nil || len(nonBranded) != 0 {
		t.Fatalf("nonBranded = %v, want empty non-nil", nonBranded)
	}
	raw, _ := json.Marshal(branded)
	if string(raw) != "[]" {
		t.Fatalf("empty branded marshals to %s, want []", string(raw))
	}
	raw, _ = json.Marshal(nonBranded)
	if string(raw) != "[]" {
		t.Fatalf("empty non-branded marshals to %s, want []", string(raw))
	}
}

func TestDecodeNewLists(t *testing.T) {
	for name, decode := range map[string]func([]byte) ([]string, error){
		"branded":     DecodeBrandedKeywords,
		"non-branded": DecodeNonBrandedKeywords,
		"competitors": DecodeBusinessCompetitors,
	} {
		got, err := decode([]byte(`["a","b"]`))
		if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("%s decode = %v, %v", name, got, err)
		}
		got, err = decode(nil)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("%s nil decode = %v, %v; want empty non-nil", name, got, err)
		}
	}
}
