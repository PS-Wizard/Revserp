package aiaudit

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func mapQueryTestRow(text, source string, ordinal int32, enabled bool) sqlc.ProjectLocationQuery {
	return sqlc.ProjectLocationQuery{
		Text: text, Normalized: strings.ToLower(strings.TrimSpace(text)),
		Kind: "map", Source: source, Origin: "service", Ordinal: ordinal, Enabled: enabled,
		LocationID: pgtype.UUID{}, ID: pgtype.UUID{},
	}
}

func TestBuildLocationMapQueryGenerationPromptStaysVerifiable(t *testing.T) {
	profile := LocationBusinessProfile{BrandName: "Acme", PrimaryCategory: "Dentist", BusinessDescription: "Family clinic.", ProductDescription: "Braces and cleanings."}
	prompt := buildLocationMapQueryGenerationPrompt(profile, LocationTarget{Address: "Thamel 1", Locality: "Kathmandu"},
		[]LocationLandmark{{Name: "Durbar Marg"}})
	for _, want := range []string{
		"Locality: Kathmandu", "Address: Thamel 1", "Category: Dentist", "Products: Braces and cleanings.",
		"Durbar Marg", "never invent", "not questions", "up to 10", "ordered best first",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestParseLocationMapQueryOutputDedupesAndDropsQuestions(t *testing.T) {
	queries, err := parseLocationMapQueryOutput(`{"queries":["dentist in Kathmandu","dentist in Kathmandu","best dentist?","  ","dentist near Durbar Marg"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(queries) != 2 || queries[0] != "dentist in Kathmandu" || queries[1] != "dentist near Durbar Marg" {
		t.Fatalf("queries = %q, want deduped Maps phrases without questions", queries)
	}
	for _, invalid := range []string{`{"queries":[]}`, `{"queries":["dentist"],}`, "Here are your queries", "1. dentist\n2. braces"} {
		if _, err := parseLocationMapQueryOutput(invalid); err == nil {
			t.Fatalf("invalid provider output must fail, got %q", invalid)
		}
	}
	if _, err := parseLocationMapQueryOutput(`{"queries":["which dentist is best?"]}`); err == nil {
		t.Fatal("questions-only output must fail")
	}
}

func TestParseLocationMapQueryOutputCapsAtTen(t *testing.T) {
	localities := []string{"Kathmandu", "Lalitpur", "Bhaktapur", "Pokhara", "Chitwan", "Butwal", "Dharan", "Biratnagar", "Nepalgunj", "Dhangadhi", "Hetauda", "Janakpur"}
	raws := make([]string, 0, len(localities))
	for _, locality := range localities {
		raws = append(raws, "\"dentist in "+locality+"\"")
	}
	queries, err := parseLocationMapQueryOutput("{\"queries\":[" + strings.Join(raws, ",") + "]}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(queries) != 10 {
		t.Fatalf("queries = %q, want best 10 only", queries)
	}
	if queries[0] != "dentist in Kathmandu" || queries[9] != "dentist in Dhangadhi" {
		t.Fatalf("queries = %q, want best-first order", queries)
	}
}

func TestPlanLocationMapQueryRefreshReplacesStaleGenerated(t *testing.T) {
	existing := []sqlc.ProjectLocationQuery{
		mapQueryTestRow("dentist in Kathmandu", "generated", 0, true),
		mapQueryTestRow("my custom query", "manual", 1, true),
		mapQueryTestRow("old generated", "generated", 2, false),
	}
	for i := range existing {
		existing[i].ID = pgtype.UUID{Bytes: [16]byte{byte(i + 1)}, Valid: true}
	}
	landmark := mapQueryTestRow("dentist near landmark", "generated", 3, false)
	landmark.Origin = "landmark"
	landmark.ID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	existing = append(existing, landmark)
	plan := planLocationMapQueryRefresh([]string{"dentist in Kathmandu", "braces in Kathmandu", "my custom query"}, existing)
	if len(plan.deleteIDs) != 2 || plan.deleteIDs[0] != existing[2].ID || plan.deleteIDs[1] != existing[3].ID {
		t.Fatalf("deleteIDs = %v, want both stale generated rows across origins", plan.deleteIDs)
	}
	if len(plan.inserts) != 1 || plan.inserts[0].Text != "braces in Kathmandu" {
		t.Fatalf("inserts = %+v, want only the missing phrase", plan.inserts)
	}
	if plan.inserts[0].Ordinal != 4 {
		t.Fatalf("ordinal = %d, want after max existing ordinal", plan.inserts[0].Ordinal)
	}
	if plan.inserts[0].Normalized == "" {
		t.Fatal("insert must carry a normalized key")
	}
	if plan.inserts[0].Enabled {
		t.Fatal("regen inserts must be disabled so paid scope never grows silently")
	}
}

func TestPlanLocationMapQueryRefreshInitialEnabledAndBounded(t *testing.T) {
	initial := planLocationMapQueryRefresh([]string{"dentist in Kathmandu", "braces in Kathmandu"}, nil)
	if len(initial.inserts) != 2 || !initial.inserts[0].Enabled || !initial.inserts[1].Enabled {
		t.Fatalf("initial starter must be enabled when no map queries exist: %+v", initial)
	}
	if len(initial.deleteIDs) != 0 {
		t.Fatalf("fresh plan must delete nothing: %+v", initial.deleteIDs)
	}
	applied := []sqlc.ProjectLocationQuery{
		mapQueryTestRow("dentist in Kathmandu", "generated", 0, true),
		mapQueryTestRow("braces in Kathmandu", "generated", 1, true),
		mapQueryTestRow("my custom query", "manual", 2, true),
	}
	repeat := planLocationMapQueryRefresh([]string{"dentist in Kathmandu", "braces in Kathmandu", "my custom query"}, applied)
	if len(repeat.inserts) != 0 || len(repeat.deleteIDs) != 0 {
		t.Fatalf("repeat plan = %+v, want empty so regeneration stays bounded", repeat)
	}
}

func TestPlanLocationMapQueryRefreshNeverCapsManual(t *testing.T) {
	var existing []sqlc.ProjectLocationQuery
	for i := 0; i < 12; i++ {
		existing = append(existing, mapQueryTestRow("manual query "+strings.Repeat("x", i+1), "manual", int32(i), true))
	}
	plan := planLocationMapQueryRefresh([]string{"fresh phrase one", "fresh phrase two"}, existing)
	if len(plan.deleteIDs) != 0 {
		t.Fatalf("manual rows must never be deleted: %v", plan.deleteIDs)
	}
	if len(plan.inserts) != 2 {
		t.Fatalf("inserts = %+v, want both fresh phrases despite 12 manual rows", plan.inserts)
	}
}

func TestPlanLocationMapQueryRefreshCapsWantAtTen(t *testing.T) {
	var generated []string
	for _, locality := range []string{"Kathmandu", "Lalitpur", "Bhaktapur", "Pokhara", "Chitwan", "Butwal", "Dharan", "Biratnagar", "Nepalgunj", "Dhangadhi", "Hetauda", "Janakpur"} {
		generated = append(generated, "dentist in "+locality)
	}
	plan := planLocationMapQueryRefresh(generated, nil)
	if len(plan.inserts) != 10 {
		t.Fatalf("inserts = %d, want max 10 even when called with 12", len(plan.inserts))
	}
	if plan.inserts[0].Text != "dentist in Kathmandu" || plan.inserts[9].Text != "dentist in Dhangadhi" {
		t.Fatalf("inserts = %+v, want best-first order", plan.inserts)
	}
}

func TestPlanLocationMapQueryRefreshTrimsStaleBeyondTen(t *testing.T) {
	localities := []string{"Kathmandu", "Lalitpur", "Bhaktapur", "Pokhara", "Chitwan", "Butwal", "Dharan", "Biratnagar", "Nepalgunj", "Dhangadhi", "Hetauda", "Janakpur", "Ilam", "Tansen", "Gorkha", "Jomsom", "Lukla", "Namche", "Jiri", "Tatopani"}
	var generated []string
	var existing []sqlc.ProjectLocationQuery
	for i, locality := range localities {
		generated = append(generated, "dentist in "+locality)
		existing = append(existing, mapQueryTestRow("dentist in "+locality, "generated", int32(i), true))
	}
	plan := planLocationMapQueryRefresh(generated, existing)
	if len(plan.deleteIDs) != 10 {
		t.Fatalf("deleteIDs = %d, want 10 stale rows beyond the best 10", len(plan.deleteIDs))
	}
	if len(plan.inserts) != 0 {
		t.Fatalf("inserts = %+v, want none when best 10 already exist", plan.inserts)
	}
}
