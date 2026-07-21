package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func existingProfileRow() sqlc.GetProjectBusinessProfileByProjectIDForUserRow {
	return sqlc.GetProjectBusinessProfileByProjectIDForUserRow{
		BrandName:           "Acme",
		WebsiteUrl:          "https://acme.example",
		PrimaryCategory:     pgtype.Text{String: "Widgets", Valid: true},
		PrimaryLocation:     pgtype.Text{String: "NYC", Valid: true},
		BusinessDescription: pgtype.Text{String: "We make widgets", Valid: true},
		SeedPrompts:         []byte(`["best widgets","widget shop"]`),
	}
}

func TestExecUpdateBusinessProfileMergesProvidedFields(t *testing.T) {
	reader := &fakeBusinessProfileReader{row: existingProfileRow()}
	var got BusinessProfileUpdate
	scope := Scope{ProjectID: testUUID(1), UserID: testUUID(2)}
	updater := func(_ context.Context, _ Scope, update BusinessProfileUpdate) error {
		got = update
		return nil
	}

	// Provide only primary_location and seed_prompts; everything else preserved.
	args := json.RawMessage(`{"primary_location":"  Boston  ","seed_prompts":[" new prompt "]}`)
	result, err := execUpdateBusinessProfile(context.Background(), scope, reader, args, updater)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if reader.gotArg.ProjectID != scope.ProjectID || reader.gotArg.UserID != scope.UserID {
		t.Fatalf("reader called with wrong scope: %+v", reader.gotArg)
	}
	if got.BrandName != "Acme" || got.WebsiteURL != "https://acme.example" {
		t.Fatalf("expected brand/website preserved, got %+v", got)
	}
	if got.PrimaryCategory != "Widgets" || got.BusinessDescription != "We make widgets" {
		t.Fatalf("expected untouched fields preserved, got %+v", got)
	}
	if got.PrimaryLocation != "Boston" {
		t.Fatalf("expected trimmed provided location, got %q", got.PrimaryLocation)
	}
	if len(got.SeedPrompts) != 1 || got.SeedPrompts[0] != "new prompt" {
		t.Fatalf("expected trimmed seed prompt replacement, got %v", got.SeedPrompts)
	}
	if result.Summary != "business profile updated" || result.Content != `{"status":"updated"}` {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestExecUpdateBusinessProfileCreatesWithRequiredFields(t *testing.T) {
	reader := &fakeBusinessProfileReader{err: pgx.ErrNoRows}
	var got BusinessProfileUpdate
	updater := func(_ context.Context, _ Scope, update BusinessProfileUpdate) error {
		got = update
		return nil
	}

	args := json.RawMessage(`{"brand_name":"  New Co  ","website_url":"https://new.example"}`)
	if _, err := execUpdateBusinessProfile(context.Background(), Scope{}, reader, args, updater); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got.BrandName != "New Co" || got.WebsiteURL != "https://new.example" {
		t.Fatalf("unexpected created profile: %+v", got)
	}
	if got.SeedPrompts == nil || len(got.SeedPrompts) != 0 {
		t.Fatalf("expected empty seed prompts slice, got %v", got.SeedPrompts)
	}
}

func TestExecUpdateBusinessProfileRejectsMissingRequiredOnCreate(t *testing.T) {
	reader := &fakeBusinessProfileReader{err: pgx.ErrNoRows}
	updater := func(context.Context, Scope, BusinessProfileUpdate) error {
		t.Fatal("updater called when validation should fail")
		return nil
	}
	// Only a description on a brand-new profile: brand_name/website_url missing.
	if _, err := execUpdateBusinessProfile(context.Background(), Scope{}, reader, json.RawMessage(`{"business_description":"desc"}`), updater); err == nil {
		t.Fatal("expected required-field error")
	}
}

func TestExecUpdateBusinessProfileRejectsTooManySeedPrompts(t *testing.T) {
	reader := &fakeBusinessProfileReader{row: existingProfileRow()}
	updater := func(context.Context, Scope, BusinessProfileUpdate) error {
		t.Fatal("updater called when seed prompts invalid")
		return nil
	}
	args := json.RawMessage(`{"seed_prompts":["1","2","3","4","5","6"]}`)
	if _, err := execUpdateBusinessProfile(context.Background(), Scope{}, reader, args, updater); err == nil {
		t.Fatal("expected too-many-prompts error")
	}
}

func TestExecUpdateBusinessProfileUnavailableWhenNil(t *testing.T) {
	reader := &fakeBusinessProfileReader{row: existingProfileRow()}
	args := json.RawMessage(`{"brand_name":"A","website_url":"https://a.example"}`)
	if _, err := execUpdateBusinessProfile(context.Background(), Scope{}, reader, args, nil); err == nil {
		t.Fatal("expected unavailable error when updater is nil")
	}
}

func TestExecUpdateBusinessProfilePropagatesFailure(t *testing.T) {
	reader := &fakeBusinessProfileReader{row: existingProfileRow()}
	want := errors.New("forbidden")
	updater := func(context.Context, Scope, BusinessProfileUpdate) error {
		return want
	}
	if _, err := execUpdateBusinessProfile(context.Background(), Scope{}, reader, json.RawMessage(`{"brand_name":"A"}`), updater); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestParseUpdateBusinessProfileArgsRejectsInvalid(t *testing.T) {
	for _, args := range []string{
		`{"brand_name":123}`,
		`{"seed_prompts":"not-an-array"}`,
		`{"seed_prompts":[1,2]}`,
		`{"unknown":"x"}`,
		`{"project_id":"x"}`,
		`[]`,
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := parseUpdateBusinessProfileArgs([]byte(args)); err == nil {
				t.Fatal("expected invalid argument error")
			}
		})
	}
}
