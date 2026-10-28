package aiaudit

import (
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/locationkeywords"
)

func TestLocationKeywordPhrasesDedupeFirstWins(t *testing.T) {
	rows := []locationkeywords.StoredKeyword{
		{Keyword: "Acme", Normalized: "acme", Kind: "brand", Source: "user"},
		{Keyword: "ACME", Normalized: "acme", Kind: "non_brand", Source: "revserp"},
		{Keyword: "emergency plumber", Normalized: "emergency plumber", Kind: "non_brand", Source: "selected"},
		{Keyword: "  ", Normalized: "  ", Kind: "brand", Source: "user"},
	}
	phrases := locationKeywordPhrases(rows)
	if len(phrases) != 2 {
		t.Fatalf("phrases = %+v, want 2 deduped entries", phrases)
	}
	if phrases[0].Keyword != "Acme" || phrases[0].Kind != "brand" {
		t.Fatalf("first phrase = %+v, want Acme/brand (first stored row wins)", phrases[0])
	}
	if phrases[1].Keyword != "emergency plumber" || phrases[1].Kind != "non_brand" {
		t.Fatalf("second phrase = %+v, want emergency plumber/non_brand", phrases[1])
	}
	if len(locationKeywordPhrases(nil)) != 0 {
		t.Fatal("empty rows must yield no phrases")
	}
}

func TestLocationAnalysisContextCarriesMarket(t *testing.T) {
	target := LocationTarget{Name: "Acme Downtown", Address: "1 Main St", Locality: "Kathmandu"}
	got := locationAnalysisContext("Acme", target, nil)
	for _, want := range []string{"Acme", "Kathmandu", "local SEO", "No crawl pages"} {
		if !strings.Contains(got, want) {
			t.Fatalf("location context missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "unknown locality") {
		t.Fatalf("known locality reported unknown: %q", got)
	}
}
