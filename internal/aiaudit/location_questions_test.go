package aiaudit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakeLocationLandmarks struct {
	landmarks     []LocationLandmark
	err           error
	calls         int
	gotLocationID pgtype.UUID
	gotProjectID  pgtype.UUID
}

func (f *fakeLocationLandmarks) GetLocationLandmarks(_ context.Context, locationID, projectID pgtype.UUID) ([]LocationLandmark, error) {
	f.calls++
	f.gotLocationID = locationID
	f.gotProjectID = projectID
	if f.err != nil {
		return nil, f.err
	}
	return f.landmarks, nil
}

type fakeLocationTargets struct {
	target LocationTarget
	err    error
	calls  int
}

func (f *fakeLocationTargets) GetLocationTarget(_ context.Context, _, _ pgtype.UUID) (LocationTarget, error) {
	f.calls++
	if f.err != nil {
		return LocationTarget{}, f.err
	}
	return f.target, nil
}

func locationTestWorker(profiles LocationProfileReader, questions LocationAIQuestionReader, targets LocationTargetReader, provider ai.Provider) *Worker {
	return locationTestWorkerWithLandmarks(profiles, questions, targets, &fakeLocationLandmarks{}, provider)
}

func locationTestWorkerWithLandmarks(profiles LocationProfileReader, questions LocationAIQuestionReader, targets LocationTargetReader, landmarks LocationLandmarkReader, provider ai.Provider) *Worker {
	return &Worker{
		cfg:               config.Config{DeepSeekModel: "test-model"},
		locationProfiles:  profiles,
		locationQuestions: questions,
		locationTargets:   targets,
		locationLandmarks: landmarks,
		newPromptProvider: func() (ai.Provider, error) { return provider, nil },
	}
}

func TestBuildLocationGenerationPromptIncludesLocalProfile(t *testing.T) {
	profile := LocationBusinessProfile{
		BrandName:           "Acme Downtown",
		WebsiteURL:          "https://acme.example",
		PrimaryCategory:     "Dentist",
		PrimaryLocation:     "Kathmandu",
		BusinessDescription: "Family dental clinic.",
		ProductDescription:  "Braces and cleanings.",
		TargetAudience:      "Local families.",
		BusinessCompetitors: []string{"CorpA"},
		Services:            []string{"Braces", "Cleanings"},
		SeedPrompts:         []string{"where downtown braces?"},
	}
	prompt := buildLocationGenerationPrompt("SYSTEM", profile, LocationTarget{}, nil)
	for _, want := range []string{
		"SYSTEM", "Brand: Acme Downtown", "Website: https://acme.example",
		"Category: Dentist", "Location: Kathmandu", "Products: Braces and cleanings.",
		"Audience: Local families.", "Competitors: CorpA", "Services: Braces, Cleanings",
		"Seed questions:", "1. where downtown braces?", "Generate 10 questions:",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The bound listing locality must drive targeting even when the copied profile
// still says a country like "United States".
func TestBuildLocationGenerationPromptUsesBoundLocationOverProfileCountry(t *testing.T) {
	profile := LocationBusinessProfile{BrandName: "Acme", PrimaryLocation: "United States"}
	target := LocationTarget{Name: "Acme Kathmandu", Locality: "Kathmandu", Localities: []string{"Kathmandu", "Lalitpur"}}
	prompt := buildLocationGenerationPrompt("SYSTEM", profile, target, nil)
	for _, want := range []string{
		"Bound location", "Name: Acme Kathmandu", "Locality: Kathmandu",
		"Localities: Kathmandu, Lalitpur", "Location: United States",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The scope is read from the claimed row, so a deleted/unknown job can never be
// mistaken for a parent job and fail the parent setup.
func TestIsLocationPromptJobUsesClaimedRowScope(t *testing.T) {
	if (&Worker{}).isLocationPromptJob(sqlc.ClaimNextPendingAIWorkerJobRow{LocationID: visibilityTestUUID(12)}) != true {
		t.Fatal("location-scoped claim row must report location")
	}
	if (&Worker{}).isLocationPromptJob(sqlc.ClaimNextPendingAIWorkerJobRow{}) {
		t.Fatal("parent claim row must not report location")
	}
}

// The location generator stores its output in the independent location scope
// and never reads or writes the parent project_ai_questions row.
func TestHandleLocationPromptGenerationStoresLocalScope(t *testing.T) {
	projectID := visibilityTestUUID(1)
	locationID := visibilityTestUUID(12)
	profiles := &fakeLocationProfiles{profile: LocationBusinessProfile{
		BrandName:       "Acme Downtown",
		PrimaryCategory: "Dentist",
		SeedPrompts:     []string{"seed"},
	}}
	questions := &fakeLocationQuestions{}
	targets := &fakeLocationTargets{target: LocationTarget{Locality: "Kathmandu"}}
	provider := &stubVisibilityProvider{respond: func(string) (string, error) {
		return "1. best downtown dentist?\n2. where to get braces?", nil
	}}
	w := locationTestWorker(profiles, questions, targets, provider)
	job := sqlc.ClaimNextPendingAIWorkerJobRow{ID: visibilityTestUUID(9), ProjectID: projectID, LocationID: locationID}

	if err := w.handleLocationPromptGeneration(context.Background(), job); err != nil {
		t.Fatalf("handleLocationPromptGeneration: %v", err)
	}
	if profiles.calls != 1 || profiles.gotLocationID != locationID || profiles.gotProjectID != projectID {
		t.Fatalf("profile load = (%d,%v,%v), want one scoped load", profiles.calls, profiles.gotLocationID, profiles.gotProjectID)
	}
	if targets.calls != 1 {
		t.Fatalf("target load = %d, want 1", targets.calls)
	}
	if provider.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls())
	}
	if !strings.Contains(provider.prompts[0], "Brand: Acme Downtown") || !strings.Contains(provider.prompts[0], "Locality: Kathmandu") {
		t.Fatalf("prompt missing local context:\n%s", provider.prompts[0])
	}
	if questions.upserts != 1 || questions.upsertLocationID != locationID || questions.upsertProjectID != projectID {
		t.Fatalf("upsert = (%d,%v,%v), want one scoped upsert", questions.upserts, questions.upsertLocationID, questions.upsertProjectID)
	}
	if len(questions.questions) != 2 || questions.questions[0] != "best downtown dentist?" {
		t.Fatalf("stored questions = %v, want parsed questions", questions.questions)
	}
	if questions.upsertModel != "test-model" {
		t.Fatalf("stored model = %q, want test-model", questions.upsertModel)
	}
}

func TestHandleLocationPromptGenerationRejectsEmptyOutput(t *testing.T) {
	profiles := &fakeLocationProfiles{profile: LocationBusinessProfile{BrandName: "Acme"}}
	questions := &fakeLocationQuestions{}
	targets := &fakeLocationTargets{}
	provider := &stubVisibilityProvider{respond: func(string) (string, error) { return "", nil }}
	w := locationTestWorker(profiles, questions, targets, provider)
	job := sqlc.ClaimNextPendingAIWorkerJobRow{ProjectID: visibilityTestUUID(1), LocationID: visibilityTestUUID(12)}
	if err := w.handleLocationPromptGeneration(context.Background(), job); err == nil {
		t.Fatal("empty provider output must fail")
	}
	if questions.upserts != 0 {
		t.Fatalf("upserts = %d, want none on failure", questions.upserts)
	}
}

func TestHandleLocationPromptGenerationRequiresLocationScope(t *testing.T) {
	w := locationTestWorker(&fakeLocationProfiles{}, &fakeLocationQuestions{}, &fakeLocationTargets{}, &stubVisibilityProvider{})
	if err := w.handleLocationPromptGeneration(context.Background(), sqlc.ClaimNextPendingAIWorkerJobRow{ProjectID: visibilityTestUUID(1)}); err == nil {
		t.Fatal("a job without location scope must fail")
	}
}

func TestBuildLocationGenerationPromptUsesSavedLandmarks(t *testing.T) {
	profile := LocationBusinessProfile{BrandName: "Acme", PrimaryLocation: "United States", Services: []string{"Braces"}}
	target := LocationTarget{Locality: "Kathmandu"}
	landmarks := []LocationLandmark{{Name: "Durbar Marg", StraightLineM: 400, Categories: []string{"shopping"}}}
	prompt := buildLocationGenerationPrompt("SYSTEM", profile, target, landmarks)
	for _, want := range []string{
		"Durbar Marg", "0.4 km", "shopping", "never invent other place names",
		"bound locality is the question geography", "Services: Braces", "Locality: Kathmandu",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildLocationGenerationPromptStaysLocalWithoutSeedsOrLandmarks(t *testing.T) {
	profile := LocationBusinessProfile{BrandName: "Acme", PrimaryLocation: "United States"}
	target := LocationTarget{Address: "Thamel 1", Locality: "Kathmandu"}
	prompt := buildLocationGenerationPrompt("SYSTEM", profile, target, nil)
	for _, want := range []string{
		"none recorded. Do not invent place names",
		"none saved. Use the bound locality and services above",
		"Address: Thamel 1",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestHandleLocationPromptGenerationLoadsSavedLandmarks(t *testing.T) {
	projectID := visibilityTestUUID(1)
	locationID := visibilityTestUUID(12)
	profiles := &fakeLocationProfiles{profile: LocationBusinessProfile{BrandName: "Acme", PrimaryLocation: "United States"}}
	questions := &fakeLocationQuestions{}
	targets := &fakeLocationTargets{target: LocationTarget{Locality: "Kathmandu"}}
	landmarks := &fakeLocationLandmarks{landmarks: []LocationLandmark{{Name: "Durbar Marg"}}}
	provider := &stubVisibilityProvider{respond: func(string) (string, error) {
		return "1. best dentist near Durbar Marg?", nil
	}}
	w := locationTestWorkerWithLandmarks(profiles, questions, targets, landmarks, provider)
	job := sqlc.ClaimNextPendingAIWorkerJobRow{ID: visibilityTestUUID(9), ProjectID: projectID, LocationID: locationID}

	if err := w.handleLocationPromptGeneration(context.Background(), job); err != nil {
		t.Fatalf("handleLocationPromptGeneration: %v", err)
	}
	if landmarks.calls != 1 || landmarks.gotLocationID != locationID || landmarks.gotProjectID != projectID {
		t.Fatalf("landmark load = (%d,%v,%v), want one scoped load", landmarks.calls, landmarks.gotLocationID, landmarks.gotProjectID)
	}
	if !strings.Contains(provider.prompts[0], "Durbar Marg") {
		t.Fatalf("prompt missing saved landmark:\n%s", provider.prompts[0])
	}
	if questions.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", questions.upserts)
	}
}

func TestHandleLocationPromptGenerationFailsWhenLandmarksUnreadable(t *testing.T) {
	profiles := &fakeLocationProfiles{profile: LocationBusinessProfile{BrandName: "Acme"}}
	questions := &fakeLocationQuestions{}
	landmarks := &fakeLocationLandmarks{err: errors.New("landmarks unavailable")}
	provider := &stubVisibilityProvider{respond: func(string) (string, error) { return "1. best dentist?", nil }}
	w := locationTestWorkerWithLandmarks(profiles, questions, &fakeLocationTargets{}, landmarks, provider)
	job := sqlc.ClaimNextPendingAIWorkerJobRow{ProjectID: visibilityTestUUID(1), LocationID: visibilityTestUUID(12)}
	if err := w.handleLocationPromptGeneration(context.Background(), job); err == nil {
		t.Fatal("unreadable landmarks must fail")
	}
	if questions.upserts != 0 {
		t.Fatalf("upserts = %d, want none on failure", questions.upserts)
	}
}
