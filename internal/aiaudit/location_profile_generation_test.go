package aiaudit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type stubScopeRow struct {
	url   pgtype.Text
	match string
	err   error
}

func (s stubScopeRow) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) != 2 {
		return errors.New("want 2 dests")
	}
	if url, ok := dest[0].(*pgtype.Text); ok {
		*url = s.url
	}
	if match, ok := dest[1].(*string); ok {
		*match = s.match
	}
	return nil
}

type stubScopeQuerier struct{ row pgx.Row }

func (s stubScopeQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return s.row }

func TestLoadLocationWebsiteScope(t *testing.T) {
	url, match, err := loadLocationWebsiteScope(context.Background(),
		stubScopeQuerier{stubScopeRow{url: pgtype.Text{String: "https://acme.example/loc", Valid: true}, match: "subtree"}}, pgtype.UUID{})
	if err != nil || url != "https://acme.example/loc" || match != "subtree" {
		t.Fatalf("scope = %q %q err %v", url, match, err)
	}
	if _, _, err := loadLocationWebsiteScope(context.Background(),
		stubScopeQuerier{stubScopeRow{err: pgx.ErrNoRows}}, pgtype.UUID{}); err != nil {
		t.Fatalf("absent scope must read empty, got %v", err)
	}
	if _, _, err := loadLocationWebsiteScope(context.Background(),
		stubScopeQuerier{stubScopeRow{err: errors.New("boom")}}, pgtype.UUID{}); err == nil {
		t.Fatal("query failure must surface, not read as absent scope")
	}
	if url, _, _ := loadLocationWebsiteScope(context.Background(),
		stubScopeQuerier{stubScopeRow{url: pgtype.Text{String: "https://acme.example", Valid: true}, match: "none"}}, pgtype.UUID{}); url != "" {
		t.Fatalf("match none must read as no scope, got %q", url)
	}
}

func TestLocationScopeLikePrefix(t *testing.T) {
	if got := locationScopeLikePrefix("https://acme.example/loc"); got != "https://acme.example/loc%" {
		t.Fatalf("prefix = %q", got)
	}
	if got := locationScopeLikePrefix("https://acme.example/100%_x"); !strings.HasSuffix(got, "%") || !strings.Contains(got, `\%`) || !strings.Contains(got, `\_`) {
		t.Fatalf("prefix must escape LIKE wildcards: %q", got)
	}
}

func TestBuildLocationProfileGenerationPromptUsesAuthoritativeGeography(t *testing.T) {
	parent := sqlc.GetProjectBusinessProfileByProjectIDRow{BrandName: "Acme", WebsiteUrl: "https://acme.example"}
	local := LocationBusinessProfile{BrandName: "Acme Downtown", BusinessDescription: "Downtown clinic.", ProductDescription: "Braces and cleanings."}
	target := LocationTarget{Name: "Acme Kathmandu", Address: "Thamel 1", Locality: "Kathmandu"}
	evidence := locationParentPageEvidence{pages: []locationProfilePageRef{{URL: "https://acme.example/about", Title: "About", MetaDescription: "About Acme"}}}
	prompt := buildLocationProfileGenerationPrompt(parent, local, target, evidence, "Evening hours differ.")
	for _, want := range []string{
		"authoritative geography", "Name: Acme Kathmandu", "Address: Thamel 1", "Locality: Kathmandu",
		"Products: Braces and cleanings.", "Evening hours differ.", "https://acme.example/about",
		"About Acme", "products description is canonical", "not a location-scoped audit",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "structured services") && strings.Contains(prompt, "Services:") {
		t.Errorf("prompt must not consult a structured services list:\n%s", prompt)
	}
}

func TestBuildLocationProfileGenerationPromptScopedEvidence(t *testing.T) {
	parent := sqlc.GetProjectBusinessProfileByProjectIDRow{BrandName: "Acme", WebsiteUrl: "https://acme.example"}
	target := LocationTarget{Locality: "Kathmandu"}
	scoped := buildLocationProfileGenerationPrompt(parent, LocationBusinessProfile{}, target,
		locationParentPageEvidence{pages: []locationProfilePageRef{{URL: "https://acme.example/loc", Title: "Loc"}}, scoped: true}, "")
	if !strings.Contains(scoped, "Selected website-scope pages") {
		t.Fatalf("scoped evidence must be labeled:\n%s", scoped)
	}
	empty := buildLocationProfileGenerationPrompt(parent, LocationBusinessProfile{}, target,
		locationParentPageEvidence{scoped: true}, "")
	if !strings.Contains(empty, "no matching stored pages") {
		t.Fatalf("scoped scope without pages must not claim absence of scope:\n%s", empty)
	}
}

func TestBuildLocationProfileGenerationPromptKeepsHomepageContextWithoutPages(t *testing.T) {
	parent := sqlc.GetProjectBusinessProfileByProjectIDRow{BrandName: "Acme", WebsiteUrl: "https://acme.example"}
	prompt := buildLocationProfileGenerationPrompt(parent, LocationBusinessProfile{}, LocationTarget{Locality: "Kathmandu"}, locationParentPageEvidence{}, "")
	if !strings.Contains(prompt, "none available. Use the parent homepage context") {
		t.Fatalf("prompt must keep homepage context without claiming a scoped audit:\n%s", prompt)
	}
}

func TestParseLocationProfileGenerationOutputKeepsBlanksForPlan(t *testing.T) {
	parsed, err := parseLocationProfileGenerationOutput("```json\n" + `{"business_description":"Local clinic.","product_description":"","target_audience":"","primary_category":"","primary_location":""}` + "\n```")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	target := LocationTarget{Address: "Thamel 1", Locality: "Kathmandu"}
	planned := planLocationProfileUpdate(LocationBusinessProfile{
		BusinessDescription: "Old.", ProductDescription: "Braces and cleanings.",
		TargetAudience: "Families.", PrimaryCategory: "Dentist", PrimaryLocation: "United States",
		BusinessCompetitors: []string{"CorpA"},
	}, target, parsed)
	if planned.BusinessDescription != "Local clinic." {
		t.Fatalf("description = %q, want adapted value", planned.BusinessDescription)
	}
	if planned.ProductDescription != "Braces and cleanings." {
		t.Fatalf("blank products output must keep stored description, got %q", planned.ProductDescription)
	}
	if planned.TargetAudience != "Families." || planned.PrimaryCategory != "Dentist" {
		t.Fatalf("blank fields must keep stored values: %+v", planned)
	}
	if planned.PrimaryLocation != "Thamel 1" {
		t.Fatalf("blank location must take the selected Maps address, got %q", planned.PrimaryLocation)
	}
	if len(planned.BusinessCompetitors) != 1 || planned.BusinessCompetitors[0] != "CorpA" {
		t.Fatalf("unmentioned competitors must keep the copy: %+v", planned.BusinessCompetitors)
	}
}

func TestLocationProfileAlwaysUsesSelectedMapsGeography(t *testing.T) {
	planned := planLocationProfileUpdate(
		LocationBusinessProfile{PrimaryLocation: "Sydney"},
		LocationTarget{Address: "Parramatta NSW"},
		locationAdaptedProfileFields{PrimaryLocation: "Melbourne"},
	)
	if planned.PrimaryLocation != "Parramatta NSW" {
		t.Fatalf("location = %q, want selected Maps geography", planned.PrimaryLocation)
	}
}

func TestParseLocationProfileGenerationOutputAdaptsMentionedCompetitors(t *testing.T) {
	parsed, err := parseLocationProfileGenerationOutput(`{"business_description":"Local clinic.","business_competitors":["Rival One","rival one","Rival Two"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.CompetitorsMentioned || len(parsed.BusinessCompetitors) != 2 {
		t.Fatalf("competitors = %+v, want deduped adapted list", parsed)
	}
	planned := planLocationProfileUpdate(LocationBusinessProfile{BusinessCompetitors: []string{"CorpA"}},
		LocationTarget{Locality: "Kathmandu"}, parsed)
	if len(planned.BusinessCompetitors) != 2 || planned.BusinessCompetitors[0] != "Rival One" {
		t.Fatalf("mentioned competitors must overwrite the copy: %+v", planned.BusinessCompetitors)
	}
}

func TestParseLocationProfileGenerationOutputRejectsBlankDescription(t *testing.T) {
	if _, err := parseLocationProfileGenerationOutput(`{"business_description":"  ","product_description":"x"}`); err == nil {
		t.Fatal("blank business_description must fail")
	}
	if _, err := parseLocationProfileGenerationOutput(`not json at all`); err == nil {
		t.Fatal("non-JSON output must fail")
	}
}
