package aiaudit

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

func TestFinalizeKeywordAnalysisSuccessAdvancesAndEnqueuesPrompt(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusKeywordAnalysis}}
	job := finalizationJob(projectID)

	if err := finalizeKeywordAnalysisSuccess(context.Background(), fake, job); err != nil {
		t.Fatalf("finalizeKeywordAnalysisSuccess: %v", err)
	}
	if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != promptGenerationJobType || fake.enqueues[0].ProjectID != projectID {
		t.Fatalf("enqueues = %+v, want exactly one prompt_generation", fake.enqueues)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(fake.updates))
	}
	if fake.updates[0].Status != projectsetup.StatusPromptGeneration || fake.updates[0].ExpectedStatus != projectsetup.StatusKeywordAnalysis {
		t.Fatalf("update = %+v, want prompt_generation from keyword_analysis", fake.updates[0])
	}
	if len(fake.completed) != 1 || fake.completed[0] != job.ID {
		t.Fatalf("completed = %+v, want the analysis job", fake.completed)
	}
}

func TestFinalizeKeywordAnalysisSuccessSetupNotOnStep(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration}}

	if err := finalizeKeywordAnalysisSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizeKeywordAnalysisSuccess: %v", err)
	}
	if len(fake.enqueues) != 0 || len(fake.updates) != 0 || len(fake.completed) != 1 {
		t.Fatalf("advanced setup must just complete: enqueues=%d updates=%d completed=%d", len(fake.enqueues), len(fake.updates), len(fake.completed))
	}
}

func TestFinalizeKeywordAnalysisSuccessNoSetupCompletes(t *testing.T) {
	fake := &fakeSetupQueries{setupErr: pgx.ErrNoRows}

	if err := finalizeKeywordAnalysisSuccess(context.Background(), fake, finalizationJob(pgtype.UUID{Bytes: [16]byte{2}, Valid: true})); err != nil {
		t.Fatalf("finalizeKeywordAnalysisSuccess: %v", err)
	}
	if len(fake.enqueues) != 0 || len(fake.updates) != 0 || len(fake.completed) != 1 {
		t.Fatalf("setup-less job must just complete")
	}
}

func TestFinalizeKeywordAnalysisFailureFailsAnalysisStep(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusKeywordAnalysis}}
	job := finalizationJob(projectID)

	if err := finalizeKeywordAnalysisFailure(context.Background(), fake, job, "no estimates"); err != nil {
		t.Fatalf("finalizeKeywordAnalysisFailure: %v", err)
	}
	if len(fake.failed) != 1 || fake.failed[0].ID != job.ID {
		t.Fatalf("failed = %+v, want the analysis job", fake.failed)
	}
	assertSetupFailedAt(t, fake.updates, projectsetup.StatusKeywordAnalysis, projectsetup.FailedStepKeywordAnalysis)
}

func TestParseKeywordEstimates(t *testing.T) {
	raw := "```json\n[{\"keyword\": \"Acme\", \"kind\": \"brand\", \"rank\": 2, \"kd\": 30.5, \"traffic\": null}]\n```"
	estimates, err := parseKeywordEstimates(raw)
	if err != nil {
		t.Fatalf("parseKeywordEstimates: %v", err)
	}
	if len(estimates) != 1 || estimates[0].Keyword != "Acme" || estimates[0].Kind != "brand" {
		t.Fatalf("estimates = %+v", estimates)
	}
	if estimates[0].Rank == nil || *estimates[0].Rank != 2 {
		t.Fatalf("rank = %+v, want 2", estimates[0].Rank)
	}
	if estimates[0].KD == nil || *estimates[0].KD != 30.5 {
		t.Fatalf("kd = %+v, want 30.5", estimates[0].KD)
	}
	if estimates[0].Traffic != nil {
		t.Fatalf("traffic = %+v, want nil", estimates[0].Traffic)
	}
	if _, err := parseKeywordEstimates("not json"); err == nil {
		t.Fatal("want error for non-JSON")
	}
}

func TestParseCompetitorKeywords(t *testing.T) {
	brand, nonBrand, err := parseCompetitorKeywords(`{"brand_keywords": ["Acme", "Acme CRM"], "non_brand_keywords": ["crm software", "lead tracking"]}`)
	if err != nil {
		t.Fatalf("parseCompetitorKeywords: %v", err)
	}
	if len(brand) != 2 || len(nonBrand) != 2 {
		t.Fatalf("brand=%v nonBrand=%v", brand, nonBrand)
	}
	if _, _, err := parseCompetitorKeywords(`{"brand_keywords": [], "non_brand_keywords": []}`); err == nil {
		t.Fatal("want error for empty lists")
	}
}

func TestValidateEstimateBatchRejectsMismatch(t *testing.T) {
	phrases := []keywordPhrase{{Keyword: "Acme", Kind: "brand"}, {Keyword: "crm software", Kind: "non_brand"}}
	full := []keywordanalysis.Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3), Traffic: int64Ptr(100), Volume: int64Ptr(500), KD: float64Ptr(20), CPC: float64Ptr(1.5)},
		{Keyword: "crm software", Kind: "non_brand", Rank: int32Ptr(8), Traffic: int64Ptr(200), Volume: int64Ptr(800), KD: float64Ptr(40), CPC: float64Ptr(2.5)},
	}
	if _, err := validateEstimateBatch(full, phrases); err != nil {
		t.Fatalf("exact batch: %v", err)
	}
	incomplete := []keywordanalysis.Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3)},
		{Keyword: "crm software", Kind: "non_brand", Rank: int32Ptr(8), Traffic: int64Ptr(200), Volume: int64Ptr(800), KD: float64Ptr(40), CPC: float64Ptr(2.5)},
	}
	if _, err := validateEstimateBatch(incomplete, phrases); err == nil {
		t.Fatal("incomplete model reply completed validation")
	}
	short := []keywordanalysis.Estimate{{Keyword: "Acme", Kind: "brand"}}
	if _, err := validateEstimateBatch(short, phrases); err == nil {
		t.Fatal("short model reply completed validation")
	}
	padded := append(append([]keywordanalysis.Estimate{}, full...), keywordanalysis.Estimate{Keyword: "extra", Kind: "brand"})
	if _, err := validateEstimateBatch(padded, phrases); err == nil {
		t.Fatal("padded model reply completed validation")
	}
	badBounds := []keywordanalysis.Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3), Traffic: int64Ptr(100), Volume: int64Ptr(500), KD: float64Ptr(101), CPC: float64Ptr(1.5)},
		{Keyword: "crm software", Kind: "non_brand", Rank: int32Ptr(8), Traffic: int64Ptr(200), Volume: int64Ptr(800), KD: float64Ptr(40), CPC: float64Ptr(2.5)},
	}
	if _, err := validateEstimateBatch(badBounds, phrases); err == nil {
		t.Fatal("out-of-bounds estimate completed validation")
	}
}

func TestKeywordAnalysisPromptRequiresBestEffortNumbers(t *testing.T) {
	for _, want := range []string{"best-effort", "never use null", "All five metrics are required"} {
		if !strings.Contains(keywordAnalysisPrompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	for _, banned := range []string{"or null", "null when unknowable", "rather than inventing", "Search Console", "results_count"} {
		if strings.Contains(keywordAnalysisPrompt, banned) {
			t.Fatalf("prompt must not contain %q", banned)
		}
	}
}

func TestEstimateContextsCarryDomain(t *testing.T) {
	pages := []sqlc.ListKeywordAnalysisPagesForCrawlRow{{Url: "https://acme.example/pricing", Title: pgtype.Text{String: "Pricing", Valid: true}}}
	project := projectAnalysisContext("Acme", "https://acme.example", pages)
	for _, want := range []string{"Acme", "https://acme.example", "https://acme.example/pricing"} {
		if !strings.Contains(project, want) {
			t.Fatalf("project context missing %q: %q", want, project)
		}
	}
	competitor := competitorAnalysisContext("Rival", "https://rival.example", nil)
	for _, want := range []string{"Rival", "https://rival.example", "No crawl pages"} {
		if !strings.Contains(competitor, want) {
			t.Fatalf("competitor context missing %q: %q", want, competitor)
		}
	}
}

func int32Ptr(v int32) *int32       { return &v }
func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }
