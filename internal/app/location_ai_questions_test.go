package app

import "testing"

func TestNormalizeLocationAIQuestions(t *testing.T) {
	got, err := normalizeLocationAIQuestions([]string{"  best dentist? ", "", "Best Dentist?", "braces near me"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"best dentist?", "braces near me"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := normalizeLocationAIQuestions([]string{" ", ""}); err == nil {
		t.Fatal("blank-only list must be rejected")
	}
}

func TestNormalizeLocationAIQuestionsCaps(t *testing.T) {
	raw := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		raw = append(raw, string(rune('a'+i))+" question")
	}
	got, err := normalizeLocationAIQuestions(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != maxLocationAIQuestions {
		t.Fatalf("len = %d, want cap %d", len(got), maxLocationAIQuestions)
	}
}
