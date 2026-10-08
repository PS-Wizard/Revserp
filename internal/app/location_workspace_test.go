package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestNormalizeWebsiteScopeInput(t *testing.T) {
	url := "https://branch.example/Menu/?x=1#top"
	stored, match, err := normalizeWebsiteScopeInput(&url, "subtree")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if match != "subtree" || stored.(string) != "https://branch.example/Menu" {
		t.Fatalf("got %v %q", stored, match)
	}
	stored, match, err = normalizeWebsiteScopeInput(nil, "none")
	if err != nil || match != "none" || stored != nil {
		t.Fatalf("none must clear url: %v %q %v", stored, match, err)
	}
	for _, tc := range []struct {
		name  string
		url   *string
		match string
	}{
		{name: "unknown match", url: &url, match: "regex"},
		{name: "exact needs url", url: nil, match: "exact"},
		{name: "blank url", url: strPtr("  "), match: "exact"},
		{name: "relative url", url: strPtr("/menu"), match: "exact"},
		{name: "encoded slash", url: strPtr("https://branch.example/a%2Fb"), match: "subtree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := normalizeWebsiteScopeInput(tc.url, tc.match); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestEqualNullableScopeURL(t *testing.T) {
	current := "https://branch.example/menu"
	if !equalNullableScopeURL(&current, "https://branch.example/menu") {
		t.Fatalf("same normalized input must be idempotent")
	}
	if equalNullableScopeURL(&current, "https://branch.example/other") {
		t.Fatalf("changed url must append a revision")
	}
	if equalNullableScopeURL(&current, nil) {
		t.Fatalf("cleared scope must append a revision")
	}
	if !equalNullableScopeURL(nil, nil) {
		t.Fatalf("repeated none must be idempotent")
	}
}

func TestNewLocationBusinessProfileResponse(t *testing.T) {
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	locationID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	resp, err := newLocationBusinessProfileResponse(id, projectID, locationID,
		"Acme", "https://acme.example",
		pgText("Cafe"), pgText("Springfield"), pgText("Family cafe"),
		pgText("Stone-baked \"Original\" loaf, 400g"), pgText("Locals"),
		[]byte(`["Big Roast"]`), []byte(`[]`), []byte(`["Catering"]`),
		pgtype.Timestamptz{}, pgtype.Timestamptz{})
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp.LocationID != locationID.String() {
		t.Fatalf("missing location_id: %+v", resp)
	}
	if resp.ProductDescription != "Stone-baked \"Original\" loaf, 400g" {
		t.Fatalf("product_description must stay verbatim: %q", resp.ProductDescription)
	}
	if len(resp.BrandedKeywords) != 0 || len(resp.NonBrandedKeywords) != 0 || len(resp.TargetKeywords) != 0 {
		t.Fatalf("location keywords start empty: %+v", resp)
	}
	if len(resp.Services) != 1 || resp.Services[0] != "Catering" {
		t.Fatalf("services snapshot missing: %+v", resp)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"brand_name"`, `"product_description"`, `"location_id"`, `"seed_prompts"`, `"services"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("response JSON missing %s: %s", key, raw)
		}
	}
}

func strPtr(value string) *string {
	return &value
}

func TestNormalizeLocationProfileServices(t *testing.T) {
	got, err := normalizeLocationProfileServices([]string{"  Catering ", "catering", "Repairs"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != 2 || got[0] != "Catering" || got[1] != "Repairs" {
		t.Fatalf("dedupe failed: %v", got)
	}
	if _, err := normalizeLocationProfileServices([]string{"   "}); err == nil {
		t.Fatalf("blank label must fail")
	}
}

func TestLocationProductDescriptionPreservesVerbatimBytes(t *testing.T) {
	exact := "  Stone-baked\nloaf,\n400g\n  "
	stored := locationProductDescription(exact)
	if !stored.Valid || stored.String != exact {
		t.Fatalf("product_description must keep exact bytes, got: %q", stored.String)
	}
	if got := locationProductDescription(""); got.Valid {
		t.Fatalf("empty description must stay null, got: %q", got.String)
	}
}
