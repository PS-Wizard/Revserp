package locationkeywords

import "testing"

func TestCombinedKeywordTextsUnionsAllSavedSources(t *testing.T) {
	rows := []StoredKeyword{
		{Keyword: "Acme", Normalized: "acme", Kind: "brand", Source: SourceUser},
		{Keyword: "Emergency Plumber", Normalized: "emergency plumber", Kind: "non_brand", Source: SourceRevserp},
		{Keyword: "Old Pick", Normalized: "old pick", Kind: "non_brand", Source: SourceSelected},
		{Keyword: "ACME", Normalized: "acme", Kind: "brand", Source: SourceSelected},
	}
	got := CombinedKeywordTexts(rows)
	if len(got) != 3 {
		t.Fatalf("combined = %#v, want 3 deduped phrases", got)
	}
	want := map[string]bool{"Acme": true, "Emergency Plumber": true, "Old Pick": true}
	for _, phrase := range got {
		if !want[phrase] {
			t.Fatalf("combined = %#v, unexpected %q", got, phrase)
		}
	}
}

func TestCombinedKeywordTextsEmpty(t *testing.T) {
	if got := CombinedKeywordTexts(nil); len(got) != 0 {
		t.Fatalf("combined = %#v, want empty", got)
	}
}
