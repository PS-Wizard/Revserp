package aichattools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

var testLocationID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}

// fakeBusinessProfileLocationReader implements businessProfileLocationReader
// without a database.
type fakeBusinessProfileLocationReader struct {
	location sqlc.GetProjectLocationForUserRow
	locErr   error
	profile  sqlc.LocationBusinessProfile
	profErr  error
}

func (f *fakeBusinessProfileLocationReader) GetProjectLocationForUser(_ context.Context, _ sqlc.GetProjectLocationForUserParams) (sqlc.GetProjectLocationForUserRow, error) {
	return f.location, f.locErr
}

func (f *fakeBusinessProfileLocationReader) GetLocationBusinessProfile(_ context.Context, _ sqlc.GetLocationBusinessProfileParams) (sqlc.LocationBusinessProfile, error) {
	return f.profile, f.profErr
}

func makeLocationProfile() sqlc.LocationBusinessProfile {
	return sqlc.LocationBusinessProfile{
		BrandName:           "Acme Springfield",
		WebsiteUrl:          "https://acme.example/springfield",
		PrimaryCategory:     text("Cafe"),
		BusinessDescription: text("Family cafe."),
		ProductDescription:  text("Stone-baked loaf, 400g"),
		BusinessCompetitors: []byte(`["Big Roast"]`),
		SeedPrompts:         []byte(`["is the patio dog friendly"]`),
		Services:            []byte(`["Catering","Repairs"]`),
	}
}

func runLocalBusinessProfile(t *testing.T, fake *fakeBusinessProfileLocationReader, raw string) Result {
	t.Helper()
	exec := businessProfileLocalExecutor{locations: fake}
	result, err := exec.runLocal(context.Background(), json.RawMessage(raw), testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal(%s) returned error: %v", raw, err)
	}
	return result
}

func TestGetLocalBusinessProfile(t *testing.T) {
	fake := &fakeBusinessProfileLocationReader{profile: makeLocationProfile()}
	result := runLocalBusinessProfile(t, fake, `{}`)
	var response businessProfileLocalResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not local profile JSON: %v\ncontent: %s", err, result.Content)
	}
	if response.BrandName != "Acme Springfield" || response.LocationID != testLocationID.String() {
		t.Fatalf("wrong identity: %+v", response)
	}
	if response.ProductDescription != "Stone-baked loaf, 400g" {
		t.Fatalf("product_description must stay verbatim: %q", response.ProductDescription)
	}
	if len(response.Services) != 2 || response.Services[0] != "Catering" {
		t.Fatalf("services snapshot missing: %+v", response.Services)
	}
	if len(response.BrandedKeywords) != 0 || len(response.TargetKeywords) != 0 {
		t.Fatalf("local keywords must start empty: %+v", response)
	}
	if response.SeedPrompts != nil {
		t.Fatalf("seed prompts hidden by default: %+v", response)
	}
}

func TestGetLocalBusinessProfileSeeds(t *testing.T) {
	fake := &fakeBusinessProfileLocationReader{profile: makeLocationProfile()}
	result := runLocalBusinessProfile(t, fake, `{"include_seed_prompts":true}`)
	var response businessProfileLocalResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not local profile JSON: %v", err)
	}
	if response.SeedPrompts == nil || len(*response.SeedPrompts) != 1 {
		t.Fatalf("seed prompts missing: %+v", response.SeedPrompts)
	}
}

func TestGetLocalBusinessProfileAbsent(t *testing.T) {
	fake := &fakeBusinessProfileLocationReader{profErr: pgx.ErrNoRows}
	result := runLocalBusinessProfile(t, fake, `{}`)
	if !strings.Contains(result.Content, "No business profile is configured for this location") {
		t.Fatalf("absent profile must explain, got: %s", result.Content)
	}
	fake = &fakeBusinessProfileLocationReader{locErr: pgx.ErrNoRows}
	result = runLocalBusinessProfile(t, fake, `{}`)
	if !strings.Contains(result.Content, "not found in this project") {
		t.Fatalf("foreign location must read as not found, got: %s", result.Content)
	}
}
