package aichattools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func newProfileWithContext() *sqlc.GetProjectBusinessProfileByProjectIDRow {
	return &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:           testProjectID,
		BrandName:           "Acme",
		WebsiteUrl:          "https://acme.example",
		ProductDescription:  pgtype.Text{String: "Old products", Valid: true},
		TargetAudience:      pgtype.Text{String: "Old audience", Valid: true},
		BusinessCompetitors: []byte(`["OldCorp"]`),
		BrandedKeywords:     []byte(`["acme"]`),
		NonBrandedKeywords:  []byte(`["widgets"]`),
		SeedPrompts:         []byte(`[]`),
		TargetKeywords:      []byte(`[]`),
	}
}

func TestUpdateBusinessProfileNewTextFields(t *testing.T) {
	store := newFakeUpdateStore("owner", newProfileWithContext())
	res, err := runPatch(t, store, `{"product_description":"  New products  ","target_audience":"SMBs"}`)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	m := parseUpdateResult(t, res)
	if m["product_description"] != "New products" || m["target_audience"] != "SMBs" {
		t.Fatalf("text fields = %v", m)
	}
	if !strings.Contains(res.Summary, "product_description") || !strings.Contains(res.Summary, "target_audience") {
		t.Fatalf("summary %q should name changed text fields", res.Summary)
	}
	// clearing with empty string
	res, err = runPatch(t, store, `{"product_description":""}`)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if m := parseUpdateResult(t, res); m["product_description"] != "" {
		t.Fatalf("cleared product_description = %v", m["product_description"])
	}
}

func TestUpdateBusinessProfileCompetitorCap(t *testing.T) {
	store := newFakeUpdateStore("owner", newProfileWithContext())
	many := make([]string, 21)
	for i := range many {
		many[i] = "Competitor " + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10))
	}
	raw, _ := json.Marshal(map[string]any{"business_competitors": many})
	res, err := runPatch(t, store, string(raw))
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	m := parseUpdateResult(t, res)
	comps, _ := m["business_competitors"].([]any)
	if len(comps) != 20 {
		t.Fatalf("competitors len = %d, want 20", len(comps))
	}
}

func TestUpdateBusinessProfileKeywordCap(t *testing.T) {
	store := newFakeUpdateStore("owner", newProfileWithContext())
	many := make([]string, 51)
	for i := range many {
		many[i] = "keyword-" + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10)) + "z"
	}
	raw, _ := json.Marshal(map[string]any{"branded_keywords": many})
	res, err := runPatch(t, store, string(raw))
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	m := parseUpdateResult(t, res)
	kw, _ := m["branded_keywords"].([]any)
	if len(kw) != 50 {
		t.Fatalf("branded len = %d, want 50", len(kw))
	}
}

func TestUpdateBusinessProfileCrossListDedupeNonBrandedWins(t *testing.T) {
	store := newFakeUpdateStore("owner", newProfileWithContext())
	res, err := runPatch(t, store, `{"branded_keywords":["Acme Shoes","Trail Boots"],"non_branded_keywords":["acme shoes","Maps"]}`)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	m := parseUpdateResult(t, res)
	branded, _ := m["branded_keywords"].([]any)
	nonBranded, _ := m["non_branded_keywords"].([]any)
	if len(nonBranded) != 2 {
		t.Fatalf("nonBranded = %v, want both kept", nonBranded)
	}
	if len(branded) != 1 || branded[0] != "Trail Boots" {
		t.Fatalf("branded = %v, want only [Trail Boots]", branded)
	}
}

func TestUpdateBusinessProfileCrossListAppliesWhenOnlyOneSideSupplied(t *testing.T) {
	profile := newProfileWithContext()
	profile.BrandedKeywords = []byte(`["Acme Shoes","Trail Boots"]`)
	profile.NonBrandedKeywords = []byte(`["widgets"]`)
	store := newFakeUpdateStore("owner", profile)
	// Supplying only non-branded with an overlap must still strip branded.
	res, err := runPatch(t, store, `{"non_branded_keywords":["widgets","ACME SHOES"]}`)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	m := parseUpdateResult(t, res)
	branded, _ := m["branded_keywords"].([]any)
	if len(branded) != 1 || branded[0] != "Trail Boots" {
		t.Fatalf("branded after one-sided patch = %v, want [Trail Boots]", branded)
	}
	if !strings.Contains(res.Summary, "branded_keywords") {
		t.Fatalf("summary %q should note the cross-list branded change", res.Summary)
	}
}

func TestUpdateBusinessProfileRejectsNullNewArrays(t *testing.T) {
	for _, raw := range []string{
		`{"business_competitors":null}`,
		`{"branded_keywords":null}`,
		`{"non_branded_keywords":null}`,
	} {
		_, err := parseUpdateBusinessProfileArgs(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "must be an array") {
			t.Fatalf("raw %s should be rejected as array, got %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"product_description":null}`,
		`{"target_audience":null}`,
	} {
		_, err := parseUpdateBusinessProfileArgs(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "must be a string") {
			t.Fatalf("raw %s should be rejected as string, got %v", raw, err)
		}
	}
}

func TestUpdateBusinessProfileEmptyPatchListsNewFields(t *testing.T) {
	_, err := parseUpdateBusinessProfileArgs(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected empty patch error")
	}
	for _, name := range []string{"product_description", "target_audience", "business_competitors", "branded_keywords", "non_branded_keywords"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("empty patch error %q should list %q", err.Error(), name)
		}
	}
}
