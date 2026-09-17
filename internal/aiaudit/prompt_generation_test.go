package aiaudit

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func fullContextProfile() sqlc.GetProjectBusinessProfileByProjectIDRow {
	return sqlc.GetProjectBusinessProfileByProjectIDRow{
		BrandName:           "Acme",
		WebsiteUrl:          "https://acme.example",
		PrimaryCategory:     pgtype.Text{String: "Widgets", Valid: true},
		PrimaryLocation:     pgtype.Text{String: "Portland, OR", Valid: true},
		BusinessDescription: pgtype.Text{String: "Sells gear.", Valid: true},
		ProductDescription:  pgtype.Text{String: "Trail widgets.", Valid: true},
		TargetAudience:      pgtype.Text{String: "Hikers.", Valid: true},
		BusinessCompetitors: []byte(`["CorpA","CorpB"]`),
		BrandedKeywords:     []byte(`["acme"]`),
		NonBrandedKeywords:  []byte(`["trail widgets","hiking gear"]`),
		SeedPrompts:         []byte(`["seed one","seed two"]`),
	}
}

func TestBuildGenerationPromptIncludesNewContext(t *testing.T) {
	prompt := buildGenerationPrompt("SYSTEM", fullContextProfile(), []string{"seed one", "seed two"})
	for _, want := range []string{
		"SYSTEM",
		"Brand: Acme",
		"Products: Trail widgets.",
		"Audience: Hikers.",
		"Competitors: CorpA, CorpB",
		"Non-branded keywords: trail widgets, hiking gear",
		"Seed questions:",
		"1. seed one",
		"Generate 10 questions:",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// Branded terms must be labeled as terms the model must avoid naming.
	if !strings.Contains(prompt, "Branded keywords") || !strings.Contains(prompt, "acme") {
		t.Errorf("prompt missing branded keywords section:\n%s", prompt)
	}
	lower := strings.ToLower(prompt)
	if !strings.Contains(lower, "never") || !strings.Contains(lower, "unprompted") {
		t.Errorf("branded section must warn against naming brand terms:\n%s", prompt)
	}
}

func TestBuildGenerationPromptOmitsEmptySections(t *testing.T) {
	profile := sqlc.GetProjectBusinessProfileByProjectIDRow{
		BrandName:  "Acme",
		WebsiteUrl: "https://acme.example",
	}
	prompt := buildGenerationPrompt("SYSTEM", profile, nil)
	for _, unwanted := range []string{
		"Products:", "Audience:", "Competitors:",
		"Branded keywords", "Non-branded keywords", "Seed questions:",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("prompt should omit empty %q section:\n%s", unwanted, prompt)
		}
	}
	if !strings.Contains(prompt, "Brand: Acme") || !strings.Contains(prompt, "Generate 10 questions:") {
		t.Errorf("prompt missing required sections:\n%s", prompt)
	}
}

func TestBuildGenerationPromptEmptySeedsHasNoDanglingHeader(t *testing.T) {
	prompt := buildGenerationPrompt("SYSTEM", fullContextProfile(), nil)
	if strings.Contains(prompt, "Seed questions:") {
		t.Errorf("empty seed list must omit the Seed questions section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Non-branded keywords:") {
		t.Errorf("profile context must still render without seeds:\n%s", prompt)
	}
	prompt = buildGenerationPrompt("SYSTEM", fullContextProfile(), []string{})
	if strings.Contains(prompt, "Seed questions:") {
		t.Errorf("empty seed slice must omit the Seed questions section:\n%s", prompt)
	}
}

// TestHandlePromptGenerationWithoutSeedsProceeds pins the removal of the hard
// "no seed prompts configured" failure: with empty seed_prompts the job must
// proceed to generation (succeeding with a working provider key, or failing
// later at the provider call) — never fail on the missing seeds.
func TestHandlePromptGenerationWithoutSeedsProceeds(t *testing.T) {
	_ = godotenv.Load("../../.env")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	defer pool.Close()
	queries := sqlc.New(pool)

	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('prompt-gen-no-seeds-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID) }()

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'prompt-gen-no-seeds','https://noseeds.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_business_profile (project_id, brand_name, website_url, business_description, product_description, target_audience, business_competitors, branded_keywords, non_branded_keywords, seed_prompts, target_keywords) VALUES ($1,'NoSeeds','https://noseeds.example','Sells gear.','Trail widgets.','Hikers.','["CorpA"]','["noseeds"]','["trail widgets"]','[]','["trail widgets"]')`, projectID); err != nil {
		t.Fatal(err)
	}

	w := &Worker{queries: queries, cfg: config.Config{}}
	if err := w.handlePromptGeneration(ctx, sqlc.ClaimNextPendingAIWorkerJobRow{ProjectID: projectID}); err != nil {
		if strings.Contains(err.Error(), "no seed prompts") {
			t.Fatalf("generation still fails on empty seeds: %v", err)
		}
		// Without a working provider key/network the provider call fails here;
		// that is expected and still proves the seed guard is gone.
		t.Logf("generation proceeded past seeds, provider-stage result: %v", err)
		return
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_ai_questions WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("questions rows = %d, want 1 after successful generation", count)
	}
}

func TestBuildGenerationPromptDoesNotTouchLocationHandling(t *testing.T) {
	// A profile without location still builds; the Maps question stays a
	// separate best-effort step handled by generateLocationQuestion.
	profile := fullContextProfile()
	profile.PrimaryLocation = pgtype.Text{}
	prompt := buildGenerationPrompt("SYSTEM", profile, []string{"seed one"})
	if strings.Contains(prompt, "Location:") {
		t.Errorf("empty location must be omitted:\n%s", prompt)
	}
}
