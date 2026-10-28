package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestSetKeywordAnalysisToolRegistered(t *testing.T) {
	registry := NewRegistry()
	if _, ok := registry.Get(setKeywordAnalysisName); !ok {
		t.Fatalf("registry names %v lack %s", registry.Names(), setKeywordAnalysisName)
	}
	found := false
	for _, def := range CatalogDefs() {
		if def.Name == setKeywordAnalysisName {
			found = true
			var schema map[string]any
			if err := json.Unmarshal(def.Schema, &schema); err != nil {
				t.Fatalf("schema is not JSON: %v", err)
			}
		}
	}
	if !found {
		t.Fatalf("catalog lacks %s", setKeywordAnalysisName)
	}
	for _, name := range LocationUnsupportedNativeTools() {
		if name == setKeywordAnalysisName {
			t.Fatalf("location turns must allow %s since the location branch", setKeywordAnalysisName)
		}
	}
	if _, ok := NewLocationScopedRegistry(nil).Get(setKeywordAnalysisName); !ok {
		t.Fatalf("location-scoped registry dropped %s", setKeywordAnalysisName)
	}
}

func TestParseSetKeywordAnalysisArgs(t *testing.T) {
	args, err := parseSetKeywordAnalysisArgs(json.RawMessage(`{
		"competitor_id": "7b2a1c00-0000-0000-0000-000000000000",
		"analyses": [
			{"keyword": "Acme", "kind": "brand", "rank": 2, "kd": 30.5, "traffic": null},
			{"keyword": "crm software", "kind": "non_brand"}
		]
	}`))
	if err != nil {
		t.Fatalf("parseSetKeywordAnalysisArgs: %v", err)
	}
	if args.CompetitorID == "" || len(args.Analyses) != 2 {
		t.Fatalf("args = %+v", args)
	}
	if args.Analyses[0].Rank == nil || *args.Analyses[0].Rank != 2 {
		t.Fatalf("rank = %+v", args.Analyses[0].Rank)
	}
	if args.Analyses[0].Traffic != nil {
		t.Fatalf("traffic must stay null, got %+v", args.Analyses[0].Traffic)
	}
	if _, err := parseSetKeywordAnalysisArgs(json.RawMessage(`{"brand_keywords": []}`)); err == nil {
		t.Fatal("want error when analyses is missing")
	}
	if _, err := parseSetKeywordAnalysisArgs(json.RawMessage(`{"analyses": [], "extra": 1}`)); err == nil {
		t.Fatal("want error for unknown argument")
	}
}

func TestEmitKeywordAnalysisUpdated(t *testing.T) {
	orgID := pgtype.UUID{Bytes: [16]byte{11}, Valid: true}
	exec := &fakeSQLExec{}
	if err := emitKeywordAnalysisUpdated(context.Background(), exec, orgID, testProjectID, "", 3); err != nil {
		t.Fatalf("emitKeywordAnalysisUpdated: %v", err)
	}
	if !strings.Contains(exec.query, "insert_organization_event") || !strings.Contains(exec.query, "keyword_analysis.updated") {
		t.Fatalf("query = %q, want durable keyword_analysis.updated event", exec.query)
	}
	if len(exec.args) != 3 {
		t.Fatalf("args = %v, want org, project, payload", exec.args)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(exec.args[2].(string)), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload["analyses"] != float64(3) {
		t.Fatalf("payload = %v, want analyses count", payload)
	}
	if _, ok := payload["competitor_id"]; ok {
		t.Fatalf("payload = %v, want no competitor_id for project scope", payload)
	}

	competitorExec := &fakeSQLExec{}
	competitorID := "7b2a1c00-0000-0000-0000-000000000000"
	if err := emitKeywordAnalysisUpdated(context.Background(), competitorExec, orgID, testProjectID, competitorID, 1); err != nil {
		t.Fatalf("emitKeywordAnalysisUpdated competitor: %v", err)
	}
	if !strings.Contains(competitorExec.args[2].(string), competitorID) {
		t.Fatalf("payload = %v, want competitor_id", competitorExec.args[2])
	}

	failing := &fakeSQLExec{err: errors.New("boom")}
	if err := emitKeywordAnalysisUpdated(context.Background(), failing, orgID, testProjectID, "", 1); err == nil {
		t.Fatal("event failure must surface so the analysis write rolls back")
	}
}
