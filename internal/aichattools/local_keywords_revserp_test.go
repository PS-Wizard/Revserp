package aichattools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/locationkeywords"
)

// The Find-keywords flow writes source revserp: the saved suggestions replace
// atomically, user-defined and selected rows stay untouched, and no Maps
// draft query syncs.
func TestLocationKeywordUpdateLocalRevserpSource(t *testing.T) {
	db := &fakeLocationKeywordDB{
		stored: []locationkeywords.StoredKeyword{
			{Keyword: "Acme", Normalized: "acme", Kind: "brand", Source: "user"},
			{Keyword: "Old Pick", Normalized: "old pick", Kind: "non_brand", Source: "selected"},
		},
		brand:    "Acme",
		services: `["Plumber"]`,
	}
	queries := testLocationAccess()
	exec := locationKeywordUpdateLocalExecutor{db: db, locations: queries}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["Acme"],"non_brand_keywords":["Emergency Plumber"],"source":"revserp"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not keyword JSON: %v\n%s", err, res.Content)
	}
	if body["source"] != "revserp" {
		t.Fatalf("response must name the written source: %s", res.Content)
	}
	if !strings.Contains(res.Summary, "suggested") || !strings.Contains(res.Summary, "1 brand, 1 non-brand") {
		t.Fatalf("summary must state scope+counts+origin: %q", res.Summary)
	}
	revserpDelete := false
	for _, stmt := range db.execs {
		if strings.Contains(stmt, "DELETE") && strings.Contains(stmt, "'revserp'") {
			revserpDelete = true
		}
		if strings.Contains(stmt, "DELETE") && (strings.Contains(stmt, "'selected'") || strings.Contains(stmt, "'user'")) {
			t.Fatalf("revserp write must not delete user/selected rows: %q", stmt)
		}
		if strings.Contains(stmt, "insert_organization_event") && !strings.Contains(stmt, "location_keywords.updated") {
			t.Fatalf("event must use the location keyword type: %q", stmt)
		}
	}
	if !revserpDelete {
		t.Fatalf("revserp write must atomically replace the revserp source: %v", db.execs)
	}
	if len(queries.inserted) != 0 || len(queries.updated) != 0 {
		t.Fatalf("revserp write must not sync Maps drafts: inserted=%v updated=%v", queries.inserted, queries.updated)
	}
	evented := false
	for _, stmt := range db.execs {
		if strings.Contains(stmt, "insert_organization_event") {
			evented = true
		}
	}
	if !evented {
		t.Fatalf("revserp write must emit an organization event: %v", db.execs)
	}
	foundLocation := false
	for _, args := range db.execArgs {
		for _, arg := range args {
			if id, ok := arg.(interface{ String() string }); ok && id.String() == testLocationID.String() {
				foundLocation = true
			}
		}
	}
	if !foundLocation {
		t.Fatalf("event and writes must carry the location id")
	}
}

func TestLocationKeywordUpdateLocalRevserpRequiresNonEmpty(t *testing.T) {
	exec := locationKeywordUpdateLocalExecutor{db: &fakeLocationKeywordDB{}, locations: testLocationAccess()}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":[],"non_brand_keywords":[],"source":"revserp"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("empty suggested lists must stay model-visible: %v", err)
	}
	if !strings.Contains(res.Content, "must not be empty") {
		t.Fatalf("content = %q, want the non-empty requirement", res.Content)
	}
}

func TestLocationKeywordUpdateLocalRejectsUnknownSource(t *testing.T) {
	exec := locationKeywordUpdateLocalExecutor{db: &fakeLocationKeywordDB{}, locations: testLocationAccess()}
	for _, raw := range []string{
		`{"brand_keywords":["a"],"non_brand_keywords":["b"],"source":"user"}`,
		`{"brand_keywords":["a"],"non_brand_keywords":["b"],"source":"bogus"}`,
		`{"brand_keywords":["a"],"non_brand_keywords":["b"],"source":null}`,
	} {
		res, err := exec.runLocal(context.Background(), json.RawMessage(raw), testProjectID, testLocationID, testUserID)
		if err != nil {
			t.Fatalf("raw %s: bad source must stay model-visible: %v", raw, err)
		}
		if !strings.Contains(res.Content, `"selected" or "revserp"`) {
			t.Fatalf("raw %s: content = %q, want the source enum", raw, res.Content)
		}
	}
}

func TestLocationKeywordUpdateLocalRevserpForbidden(t *testing.T) {
	queries := testLocationAccess()
	queries.member.Role = "member"
	exec := locationKeywordUpdateLocalExecutor{db: &fakeLocationKeywordDB{}, locations: queries}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["Acme"],"non_brand_keywords":["Emergency Plumber"],"source":"revserp"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("denial must stay model-visible: %v", err)
	}
	if !strings.Contains(res.Content, "only organization owners") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestLocationKeywordUpdateLocalRequiresExplicitSource(t *testing.T) {
	if _, err := parseLocalKeywordArgs(json.RawMessage(`{"brand_keywords":["a"],"non_brand_keywords":["b"]}`)); err == nil {
		t.Fatal("omitted source must fail so suggestions never auto-select")
	}
	args, err := parseLocalKeywordArgs(json.RawMessage(`{"brand_keywords":["a"],"non_brand_keywords":["b"],"source":"selected"}`))
	if err != nil {
		t.Fatalf("parse explicit selected: %v", err)
	}
	if args.Source != locationkeywords.SourceSelected {
		t.Fatalf("source = %q, want explicit selected", args.Source)
	}
}

func TestLocationKeywordUpdateLocalOmittedSourceWritesNothing(t *testing.T) {
	db := &fakeLocationKeywordDB{}
	queries := testLocationAccess()
	exec := locationKeywordUpdateLocalExecutor{db: db, locations: queries}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["Acme"],"non_brand_keywords":["Emergency Plumber"]}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("missing source must stay model-visible: %v", err)
	}
	if !strings.Contains(res.Content, `"source" is required`) {
		t.Fatalf("content = %q, want the required-source error", res.Content)
	}
	if len(db.execs) != 0 {
		t.Fatalf("missing source must not write: %v", db.execs)
	}
	if len(queries.inserted) != 0 || len(queries.updated) != 0 {
		t.Fatalf("missing source must not sync Maps drafts: inserted=%v updated=%v", queries.inserted, queries.updated)
	}
}

// The location-scoped tool schema requires the source so generated or
// suggested keywords never auto-select; the project schema stays
// source-free (covered by TestProjectKeywordToolsRegistryAndCatalog).
func TestLocationKeywordToolSchemaAdvertisesSource(t *testing.T) {
	local, ok := NewLocationScopedRegistry(nil).Get(updateProjectKeywordsName)
	if !ok {
		t.Fatalf("%s not served in location mode", updateProjectKeywordsName)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(local.Def.Schema, &schema); err != nil {
		t.Fatalf("location keyword schema is not valid JSON: %v", err)
	}
	if len(schema.Required) != 3 {
		t.Fatalf("source must stay required: required = %v", schema.Required)
	}
	source, ok := schema.Properties["source"]
	if !ok {
		t.Fatal("location keyword schema must advertise the source target")
	}
	if len(source.Enum) != 2 || source.Enum[0] != "selected" || source.Enum[1] != "revserp" {
		t.Fatalf("source enum = %v, want [selected revserp]", source.Enum)
	}
}

// Saved revserp rows surface in the suggested card together with the derived
// set; phrases the derived set cannot attribute carry no guessed origin.
func TestLocationKeywordListsLocalMergesSavedSuggestions(t *testing.T) {
	db := &fakeLocationKeywordDB{
		stored: []locationkeywords.StoredKeyword{
			{Keyword: "Emergency Plumber", Normalized: "emergency plumber", Kind: "non_brand", Source: "revserp"},
			{Keyword: "Acme", Normalized: "acme", Kind: "brand", Source: "user"},
		},
		brand:    "Acme",
		services: `["Plumber"]`,
	}
	queries := testLocationAccess()
	exec := locationKeywordListsLocalExecutor{db: db, locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var lists map[string]locationkeywords.KeywordGroup
	if err := json.Unmarshal([]byte(res.Content), &lists); err != nil {
		t.Fatalf("content is not keyword JSON: %v\n%s", err, res.Content)
	}
	foundSaved, foundDerived := false, false
	for _, phrase := range lists["revserp_suggested"].NonBranded {
		if phrase == "Emergency Plumber" {
			foundSaved = true
		}
		if phrase == "Plumber in Springfield" {
			foundDerived = true
		}
	}
	if !foundSaved || !foundDerived {
		t.Fatalf("suggested card must combine saved and derived: %s", res.Content)
	}
	var payload struct {
		SuggestedOrigins locationkeywords.SuggestedOrigins `json:"suggested_origins"`
	}
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("content origins: %v", err)
	}
	if _, ok := payload.SuggestedOrigins["emergency plumber"]; ok {
		t.Fatalf("saved-only phrases must carry no guessed origin: %s", res.Content)
	}
}
