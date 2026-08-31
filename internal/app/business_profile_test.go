package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNormalizeTargetKeywordsTrimming(t *testing.T) {
	got := normalizeTargetKeywords([]string{"  seo  ", "\tmaps ", " keyword"})
	want := []string{"seo", "maps", "keyword"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trimming: got %v want %v", got, want)
	}
}

func TestNormalizeTargetKeywordsEmptyRemoval(t *testing.T) {
	got := normalizeTargetKeywords([]string{"seo", "   ", "", "\t", "maps", " "})
	want := []string{"seo", "maps"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty removal: got %v want %v", got, want)
	}
}

func TestNormalizeTargetKeywordsCaseInsensitiveDedupe(t *testing.T) {
	got := normalizeTargetKeywords([]string{"SEO", "seo", "Seo", "Maps", "maps", " MAPS "})
	want := []string{"SEO", "Maps"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe: got %v want %v", got, want)
	}
}

func TestNormalizeTargetKeywordsPreservesFirstSpellingAndOrder(t *testing.T) {
	got := normalizeTargetKeywords([]string{"  Hello World ", "hello world", "HELLO WORLD", "  Go  ", "go"})
	want := []string{"Hello World", "Go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preserve: got %v want %v", got, want)
	}
}

func TestNormalizeTargetKeywordsEmptyArray(t *testing.T) {
	got := normalizeTargetKeywords(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("nil input: got %v want empty non-nil slice", got)
	}
	// Ensure JSON marshals to [] not null
	raw, _ := json.Marshal(got)
	if string(raw) != "[]" {
		t.Fatalf("empty marshal: got %s want []", string(raw))
	}

	got = normalizeTargetKeywords([]string{})
	raw, _ = json.Marshal(got)
	if string(raw) != "[]" {
		t.Fatalf("empty slice marshal: got %s want []", string(raw))
	}
}

func TestDecodeTargetKeywords(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want []string
	}{
		{name: "empty nil", raw: nil, want: []string{}},
		{name: "empty bytes", raw: []byte{}, want: []string{}},
		{name: "empty array", raw: []byte(`[]`), want: []string{}},
		{name: "null", raw: []byte(`null`), want: []string{}},
		{name: "values", raw: []byte(`["seo","maps"]`), want: []string{"seo", "maps"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeTargetKeywords(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDecodeStringSliceShared(t *testing.T) {
	raw := []byte(`["a","b"]`)
	a, err := decodeSeedPrompts(raw)
	if err != nil {
		t.Fatalf("decodeSeedPrompts: %v", err)
	}
	b, err := decodeTargetKeywords(raw)
	if err != nil {
		t.Fatalf("decodeTargetKeywords: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("shared decode mismatch: %v vs %v", a, b)
	}
}

func TestNewProjectBusinessProfileResponseTargetKeywords(t *testing.T) {
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	created := pgtype.Timestamptz{Time: pgtype.Timestamptz{}.Time, Valid: true}
	updated := pgtype.Timestamptz{Time: pgtype.Timestamptz{}.Time, Valid: true}

	// Old row default: target_keywords '[]'
	row := sqlc.GetProjectBusinessProfileByProjectIDRow{
		ID:             id,
		ProjectID:      projectID,
		BrandName:      "Acme",
		WebsiteUrl:     "https://acme.example",
		SeedPrompts:    []byte(`[]`),
		TargetKeywords: []byte(`[]`),
		CreatedAt:      created,
		UpdatedAt:      updated,
	}
	resp, err := newProjectBusinessProfileResponseFromGetRow(row)
	if err != nil {
		t.Fatalf("response from get row: %v", err)
	}
	if resp.TargetKeywords == nil || len(resp.TargetKeywords) != 0 {
		t.Fatalf("expected empty target_keywords, got %v", resp.TargetKeywords)
	}

	// With keywords
	row.TargetKeywords = []byte(`["SEO","maps"]`)
	resp, err = newProjectBusinessProfileResponseFromGetRow(row)
	if err != nil {
		t.Fatalf("response with keywords: %v", err)
	}
	if !reflect.DeepEqual(resp.TargetKeywords, []string{"SEO", "maps"}) {
		t.Fatalf("got %v want [SEO maps]", resp.TargetKeywords)
	}

	// Nil raw (simulates missing before migration fallback len==0)
	resp2, err := newProjectBusinessProfileResponse(id, projectID, "Acme", "https://acme.example", pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, []byte(`[]`), nil, created, updated)
	if err != nil {
		t.Fatalf("nil target keywords decode: %v", err)
	}
	if len(resp2.TargetKeywords) != 0 {
		t.Fatalf("nil raw want empty, got %v", resp2.TargetKeywords)
	}
}
