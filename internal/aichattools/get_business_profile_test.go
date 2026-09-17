package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// fakeBusinessProfileReader implements businessProfileReader without a database.
type fakeBusinessProfileReader struct {
	profile sqlc.GetProjectBusinessProfileByProjectIDForUserRow
	err     error
}

func (f *fakeBusinessProfileReader) GetProjectBusinessProfileByProjectIDForUser(_ context.Context, _ sqlc.GetProjectBusinessProfileByProjectIDForUserParams) (sqlc.GetProjectBusinessProfileByProjectIDForUserRow, error) {
	return f.profile, f.err
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func makeProfile() sqlc.GetProjectBusinessProfileByProjectIDForUserRow {
	return sqlc.GetProjectBusinessProfileByProjectIDForUserRow{
		BrandName:           "Acme LLC",
		WebsiteUrl:          "https://acme.example",
		PrimaryCategory:     text("E-commerce"),
		PrimaryLocation:     text("Portland, OR"),
		BusinessDescription: text("Sells handmade outdoor gear."),
		SeedPrompts:         []byte(`["who buys acme gear","is acme's warranty transferable"]`),
		TargetKeywords:      []byte(`["seo","maps"]`),
	}
}

func runBusinessProfile(t *testing.T, fake *fakeBusinessProfileReader, raw string) Result {
	t.Helper()
	exec := businessProfileExecutor{profiles: fake}
	result, err := exec.run(context.Background(), json.RawMessage(raw), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("run(%s) returned error: %v", raw, err)
	}
	return result
}

func decodeBusinessProfile(t *testing.T, result Result) businessProfileResponse {
	t.Helper()
	var response businessProfileResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not valid profile JSON: %v\ncontent: %s", err, result.Content)
	}
	return response
}

func TestBusinessProfileDefaultsExcludeSeedPrompts(t *testing.T) {
	fake := &fakeBusinessProfileReader{profile: makeProfile()}
	result := runBusinessProfile(t, fake, `{}`)
	response := decodeBusinessProfile(t, result)

	if response.BrandName != "Acme LLC" || response.WebsiteURL != "https://acme.example" {
		t.Fatalf("brand/website = %q/%q, want Acme LLC/https://acme.example", response.BrandName, response.WebsiteURL)
	}
	if response.PrimaryCategory != "E-commerce" || response.PrimaryLocation != "Portland, OR" {
		t.Fatalf("category/location = %q/%q", response.PrimaryCategory, response.PrimaryLocation)
	}
	if response.BusinessDescription != "Sells handmade outdoor gear." {
		t.Fatalf("description = %q", response.BusinessDescription)
	}
	if response.SeedPrompts != nil {
		t.Fatalf("SeedPrompts = %v, want omitted by default", response.SeedPrompts)
	}
	if response.TargetKeywords == nil || len(response.TargetKeywords) != 2 || response.TargetKeywords[0] != "seo" {
		t.Fatalf("TargetKeywords = %v, want [seo maps] always", response.TargetKeywords)
	}
	if want := "business profile: Acme LLC (E-commerce)"; result.Summary != want {
		t.Fatalf("Summary = %q, want %q", result.Summary, want)
	}
}

func TestBusinessProfileIncludeSeedPrompts(t *testing.T) {
	fake := &fakeBusinessProfileReader{profile: makeProfile()}
	response := decodeBusinessProfile(t, runBusinessProfile(t, fake, `{"include_seed_prompts":true}`))
	if len(response.SeedPrompts) != 2 || response.SeedPrompts[0] != "who buys acme gear" {
		t.Fatalf("SeedPrompts = %+v, want the two configured prompts", response.SeedPrompts)
	}
}

func TestBusinessProfileNotConfigured(t *testing.T) {
	fake := &fakeBusinessProfileReader{err: pgx.ErrNoRows}
	result := runBusinessProfile(t, fake, `{}`)
	if !strings.Contains(result.Content, "No business profile is configured") {
		t.Fatalf("Content = %q, want not-configured explanation", result.Content)
	}
	if result.Summary != "business profile not configured" {
		t.Fatalf("Summary = %q, want business profile not configured", result.Summary)
	}
}

func TestBusinessProfileCaps(t *testing.T) {
	fake := &fakeBusinessProfileReader{profile: sqlc.GetProjectBusinessProfileByProjectIDForUserRow{
		BrandName:           "Acme LLC",
		WebsiteUrl:          "https://acme.example",
		BusinessDescription: text(strings.Repeat("d", 700)),
		SeedPrompts: func() []byte {
			prompts := make([]string, 40)
			for i := range prompts {
				prompts[i] = strings.Repeat("p", 300)
			}
			raw, _ := json.Marshal(prompts)
			return raw
		}(),
	}}
	response := decodeBusinessProfile(t, runBusinessProfile(t, fake, `{"include_seed_prompts":true}`))
	if want := strings.Repeat("d", 500) + "\u2026"; response.BusinessDescription != want {
		t.Fatalf("description = %q, want capped at 500 with marker", response.BusinessDescription)
	}
	if len(response.SeedPrompts) != businessProfileMaxPromptCount {
		t.Fatalf("SeedPrompts = %d entries, want capped at %d", len(response.SeedPrompts), businessProfileMaxPromptCount)
	}
	if want := strings.Repeat("p", 200) + "\u2026"; response.SeedPrompts[0] != want {
		t.Fatalf("seed prompt[0] not capped at 200 with marker")
	}
}

func TestBusinessProfileParseArgs(t *testing.T) {
	tests := []struct {
		raw     string
		want    businessProfileArgs
		wantErr string
	}{
		{raw: `{}`, want: businessProfileArgs{}},
		{raw: ``, want: businessProfileArgs{}},
		{raw: `{"include_seed_prompts":true}`, want: businessProfileArgs{IncludeSeedPrompts: true}},
		{raw: `{"include_seed_prompts":"yes"}`, wantErr: "must be a boolean"},
		{raw: `{"bogus":1}`, wantErr: `unknown argument "bogus"`},
		{raw: `{"include_seed_prompts":true,"include_seed_prompts":false}`, wantErr: `duplicate argument`},
		{raw: `{} {}`, wantErr: "trailing data"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, err := parseBusinessProfileArgs(json.RawMessage(test.raw))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseBusinessProfileArgs(%s) error = %v, want containing %q", test.raw, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBusinessProfileArgs(%s) error = %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("parseBusinessProfileArgs(%s) = %+v, want %+v", test.raw, got, test.want)
			}
		})
	}
}

func TestBusinessProfileDatabaseErrorsPropagate(t *testing.T) {
	fake := &fakeBusinessProfileReader{err: errors.New("boom")}
	exec := businessProfileExecutor{profiles: fake}
	_, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err == nil || !strings.Contains(err.Error(), "read profile") {
		t.Fatalf("run() error = %v, want wrapped read-profile error", err)
	}
}

func TestExecuteBusinessProfileRequiresQueries(t *testing.T) {
	_, err := executeGetBusinessProfile(context.Background(), json.RawMessage(`{}`), Scope{})
	if err == nil || !strings.Contains(err.Error(), "no queries") {
		t.Fatalf("executeGetBusinessProfile error = %v, want no-queries error", err)
	}
}

func TestBusinessProfileToolDef(t *testing.T) {
	tool := getBusinessProfileTool()
	if tool.Def.Name != businessProfileName {
		t.Fatalf("tool name = %q, want %q", tool.Def.Name, businessProfileName)
	}
	if tool.Def.Label == "" || tool.Def.Description == "" || len(tool.Def.Schema) == 0 {
		t.Fatalf("tool def incomplete: %+v", tool.Def)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties missing")
	}
	if len(properties) != 1 {
		t.Fatalf("schema properties = %v, want only include_seed_prompts", properties)
	}
	if _, ok := properties["include_seed_prompts"]; !ok {
		t.Fatal("schema must accept include_seed_prompts")
	}
}
