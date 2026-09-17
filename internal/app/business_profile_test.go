package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

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
	resp2, err := newProjectBusinessProfileResponse(id, projectID, "Acme", "https://acme.example", pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, []byte(`[]`), []byte(`[]`), []byte(`[]`), []byte(`[]`), nil, created, updated)
	if err != nil {
		t.Fatalf("nil target keywords decode: %v", err)
	}
	if len(resp2.TargetKeywords) != 0 {
		t.Fatalf("nil raw want empty, got %v", resp2.TargetKeywords)
	}
}

func TestNewProjectBusinessProfileResponseNewFields(t *testing.T) {
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	created := pgtype.Timestamptz{Time: pgtype.Timestamptz{}.Time, Valid: true}
	updated := pgtype.Timestamptz{Time: pgtype.Timestamptz{}.Time, Valid: true}

	row := sqlc.GetProjectBusinessProfileByProjectIDRow{
		ID:                  id,
		ProjectID:           projectID,
		BrandName:           "Acme",
		WebsiteUrl:          "https://acme.example",
		ProductDescription:  pgtype.Text{String: "Widgets", Valid: true},
		TargetAudience:      pgtype.Text{String: "Hikers", Valid: true},
		BusinessCompetitors: []byte(`["CorpA","CorpB"]`),
		BrandedKeywords:     []byte(`["acme"]`),
		NonBrandedKeywords:  []byte(`["widgets"]`),
		SeedPrompts:         []byte(`[]`),
		TargetKeywords:      []byte(`["kw"]`),
		CreatedAt:           created,
		UpdatedAt:           updated,
	}
	resp, err := newProjectBusinessProfileResponseFromGetRow(row)
	if err != nil {
		t.Fatalf("response from get row: %v", err)
	}
	if resp.ProductDescription != "Widgets" || resp.TargetAudience != "Hikers" {
		t.Fatalf("text fields = %q/%q", resp.ProductDescription, resp.TargetAudience)
	}
	if !reflect.DeepEqual(resp.BusinessCompetitors, []string{"CorpA", "CorpB"}) {
		t.Fatalf("competitors = %v", resp.BusinessCompetitors)
	}
	if !reflect.DeepEqual(resp.BrandedKeywords, []string{"acme"}) {
		t.Fatalf("branded = %v", resp.BrandedKeywords)
	}
	if !reflect.DeepEqual(resp.NonBrandedKeywords, []string{"widgets"}) {
		t.Fatalf("nonBranded = %v", resp.NonBrandedKeywords)
	}
	if !reflect.DeepEqual(resp.TargetKeywords, []string{"kw"}) {
		t.Fatalf("targetKeywords = %v, must stay untouched", resp.TargetKeywords)
	}

	upserted := sqlc.UpsertProjectBusinessProfileRow{
		ID:                  id,
		ProjectID:           projectID,
		BrandName:           "Acme",
		WebsiteUrl:          "https://acme.example",
		BusinessCompetitors: []byte(`not json`),
		BrandedKeywords:     nil,
		NonBrandedKeywords:  []byte(`[]`),
		SeedPrompts:         []byte(`[]`),
		TargetKeywords:      []byte(`[]`),
		CreatedAt:           created,
		UpdatedAt:           updated,
	}
	if _, err := newProjectBusinessProfileResponseFromUpsertRow(upserted); err == nil {
		t.Fatal("corrupt business_competitors should surface a decode error")
	}
}

// The profile tests above are all struct-in/struct-out, so the 13-parameter
// upsert was never exercised against Postgres. A wrong column mapping or a
// JSONB encoding mistake would pass every one of them. This closes that gap.
func TestProjectBusinessProfileRoundTripsNewFieldsAgainstDB(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	name := fmt.Sprintf("business-profile-db-test-%d", time.Now().UnixNano())
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, $2, 'https://example.com') RETURNING id`, orgID, name).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}

	params := sqlc.UpsertProjectBusinessProfileParams{
		ProjectID:           projectID,
		BrandName:           "Roundtrip Co",
		WebsiteUrl:          "https://roundtrip.example",
		PrimaryCategory:     pgtype.Text{String: "Widgets", Valid: true},
		PrimaryLocation:     pgtype.Text{String: "Kathmandu, Nepal", Valid: true},
		BusinessDescription: pgtype.Text{String: "We sell widgets.", Valid: true},
		ProductDescription:  pgtype.Text{String: "Trail widgets and boots.", Valid: true},
		TargetAudience:      pgtype.Text{String: "Weekend hikers", Valid: true},
		BusinessCompetitors: []byte(`["CorpA","CorpB"]`),
		BrandedKeywords:     []byte(`["roundtrip co"]`),
		NonBrandedKeywords:  []byte(`["trail widgets"]`),
		SeedPrompts:         []byte(`["best trail widgets"]`),
		TargetKeywords:      []byte(`["target kw"]`),
	}
	if _, err := queries.UpsertProjectBusinessProfile(ctx, params); err != nil {
		t.Fatalf("upsert (insert path): %v", err)
	}

	row, err := queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		t.Fatalf("get after insert: %v", err)
	}
	if row.ProductDescription.String != "Trail widgets and boots." || !row.ProductDescription.Valid {
		t.Errorf("product_description = %+v", row.ProductDescription)
	}
	if row.TargetAudience.String != "Weekend hikers" || !row.TargetAudience.Valid {
		t.Errorf("target_audience = %+v", row.TargetAudience)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want []string
	}{
		{"business_competitors", row.BusinessCompetitors, []string{"CorpA", "CorpB"}},
		{"branded_keywords", row.BrandedKeywords, []string{"roundtrip co"}},
		{"non_branded_keywords", row.NonBrandedKeywords, []string{"trail widgets"}},
		{"target_keywords (must stay untouched)", row.TargetKeywords, []string{"target kw"}},
	} {
		var got []string
		if err := json.Unmarshal(tc.raw, &got); err != nil {
			t.Errorf("%s did not round-trip as JSON: %v (raw %q)", tc.name, err, string(tc.raw))
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}

	// ON CONFLICT path: every new column must be overwritten, not stale.
	params.ProductDescription = pgtype.Text{String: "Changed products.", Valid: true}
	params.TargetAudience = pgtype.Text{}
	params.BrandedKeywords = []byte(`["new brand"]`)
	params.NonBrandedKeywords = []byte(`["changed non branded"]`)
	params.BusinessCompetitors = []byte(`[]`)
	if _, err := queries.UpsertProjectBusinessProfile(ctx, params); err != nil {
		t.Fatalf("upsert (conflict path): %v", err)
	}

	updated, err := queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if updated.ProductDescription.String != "Changed products." {
		t.Errorf("product_description not overwritten: %+v", updated.ProductDescription)
	}
	if updated.TargetAudience.Valid {
		t.Errorf("target_audience should have been cleared, got %+v", updated.TargetAudience)
	}
	var branded []string
	if err := json.Unmarshal(updated.BrandedKeywords, &branded); err != nil || !reflect.DeepEqual(branded, []string{"new brand"}) {
		t.Errorf("branded_keywords not overwritten: %v (%q)", err, string(updated.BrandedKeywords))
	}
	var competitors []string
	if err := json.Unmarshal(updated.BusinessCompetitors, &competitors); err != nil || len(competitors) != 0 {
		t.Errorf("business_competitors not cleared: %v (%q)", err, string(updated.BusinessCompetitors))
	}
	var nonBranded []string
	if err := json.Unmarshal(updated.NonBrandedKeywords, &nonBranded); err != nil || !reflect.DeepEqual(nonBranded, []string{"changed non branded"}) {
		t.Errorf("non_branded_keywords not overwritten: %v (%q)", err, string(updated.NonBrandedKeywords))
	}
}
