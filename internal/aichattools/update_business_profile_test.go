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

type fakeUpdateStore struct {
	projectID pgtype.UUID
	userID    pgtype.UUID
	orgID     pgtype.UUID
	role      string
	profile   *sqlc.GetProjectBusinessProfileByProjectIDRow
	enqueued  int
}

func newFakeUpdateStore(role string, profile *sqlc.GetProjectBusinessProfileByProjectIDRow) *fakeUpdateStore {
	if role == "" {
		role = "owner"
	}
	return &fakeUpdateStore{
		projectID: testProjectID,
		userID:    testUserID,
		orgID:     pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		role:      role,
		profile:   profile,
	}
}

func (f *fakeUpdateStore) GetProjectByIDForUserForBusinessProfileUpdate(_ context.Context, arg sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams) (sqlc.Project, error) {
	if arg.ID != f.projectID || arg.UserID != f.userID {
		return sqlc.Project{}, pgx.ErrNoRows
	}
	return sqlc.Project{ID: f.projectID, OrganizationID: f.orgID}, nil
}

func (f *fakeUpdateStore) GetOrganizationMember(_ context.Context, arg sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error) {
	if arg.OrgID != f.orgID || arg.UserID != f.userID {
		return sqlc.OrganizationMember{}, pgx.ErrNoRows
	}
	return sqlc.OrganizationMember{OrgID: f.orgID, UserID: f.userID, Role: f.role}, nil
}

func (f *fakeUpdateStore) GetProjectBusinessProfileByProjectID(_ context.Context, projectID pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, error) {
	if f.profile == nil || f.profile.ProjectID != projectID {
		return sqlc.GetProjectBusinessProfileByProjectIDRow{}, pgx.ErrNoRows
	}
	return *f.profile, nil
}

func (f *fakeUpdateStore) UpsertProjectBusinessProfile(_ context.Context, arg sqlc.UpsertProjectBusinessProfileParams) (sqlc.UpsertProjectBusinessProfileRow, error) {
	row := sqlc.GetProjectBusinessProfileByProjectIDRow{
		ID:                  pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
		ProjectID:           arg.ProjectID,
		BrandName:           arg.BrandName,
		WebsiteUrl:          arg.WebsiteUrl,
		PrimaryCategory:     arg.PrimaryCategory,
		PrimaryLocation:     arg.PrimaryLocation,
		BusinessDescription: arg.BusinessDescription,
		SeedPrompts:         arg.SeedPrompts,
		TargetKeywords:      arg.TargetKeywords,
	}
	f.profile = &row
	return sqlc.UpsertProjectBusinessProfileRow{
		ID:                  row.ID,
		ProjectID:           row.ProjectID,
		BrandName:           row.BrandName,
		WebsiteUrl:          row.WebsiteUrl,
		PrimaryCategory:     row.PrimaryCategory,
		PrimaryLocation:     row.PrimaryLocation,
		BusinessDescription: row.BusinessDescription,
		SeedPrompts:         row.SeedPrompts,
		TargetKeywords:      row.TargetKeywords,
	}, nil
}

func (f *fakeUpdateStore) EnqueueAIWorkerJob(_ context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error) {
	f.enqueued++
	return sqlc.EnqueueAIWorkerJobRow{JobType: arg.JobType, ProjectID: arg.ProjectID}, nil
}

// helper that tests pure patch logic via fake querier without transaction.
func runPatch(t *testing.T, store *fakeUpdateStore, raw string) (Result, error) {
	t.Helper()
	exec := updateBusinessProfileExecutor{queries: &sqlc.Queries{}, db: &fakeTransactor{}}
	args, err := parseUpdateBusinessProfileArgs(json.RawMessage(raw))
	if err != nil {
		return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil
	}
	return exec.patch(context.Background(), args, testProjectID, testUserID, store)
}

type fakeTransactor struct{}

func (f *fakeTransactor) Begin(_ context.Context) (pgx.Tx, error) { return nil, errors.New("unused") }

func parseUpdateResult(t *testing.T, result Result) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &m); err != nil {
		t.Fatalf("content is not JSON: %v\ncontent: %s", err, result.Content)
	}
	return m
}

func TestUpdateBusinessProfileParseUnknownFields(t *testing.T) {
	_, err := parseUpdateBusinessProfileArgs(json.RawMessage(`{"brand_name":"a","unknown":1}`))
	if err == nil || !strings.Contains(err.Error(), `unknown argument "unknown"`) {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestUpdateBusinessProfileParseEmptyPatch(t *testing.T) {
	_, err := parseUpdateBusinessProfileArgs(json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "no fields provided") {
		t.Fatalf("expected empty patch error, got %v", err)
	}
	_, err = parseUpdateBusinessProfileArgs(json.RawMessage(``))
	if err == nil || !strings.Contains(err.Error(), "no fields provided") {
		t.Fatalf("empty input should be empty patch, got %v", err)
	}
}

func TestUpdateBusinessProfileRejectsNullArrays(t *testing.T) {
	_, err := parseUpdateBusinessProfileArgs(json.RawMessage(`{"seed_prompts":null}`))
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("seed null should be rejected, got %v", err)
	}
	_, err = parseUpdateBusinessProfileArgs(json.RawMessage(`{"target_keywords":null}`))
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("keywords null should be rejected, got %v", err)
	}
}

func TestUpdateBusinessProfileRejectsNullScalars(t *testing.T) {
	_, err := parseUpdateBusinessProfileArgs(json.RawMessage(`{"brand_name":null}`))
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("brand null should be rejected, got %v", err)
	}
	_, err = parseUpdateBusinessProfileArgs(json.RawMessage(`{"website_url":null}`))
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("website null should be rejected, got %v", err)
	}
	_, err = parseUpdateBusinessProfileArgs(json.RawMessage(`{"primary_category":null}`))
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("category null should be rejected, got %v", err)
	}
}

func TestUpdateBusinessProfileRejectsTenantIDs(t *testing.T) {
	for _, raw := range []string{
		`{"project_id":"123"}`,
		`{"user_id":"123"}`,
		`{"organization_id":"123"}`,
		`{"org_id":"123"}`,
	} {
		_, err := parseUpdateBusinessProfileArgs(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "unknown argument") {
			t.Fatalf("raw %s should be rejected as unknown, got %v", raw, err)
		}
	}
}

func TestUpdateBusinessProfileRequiresDB(t *testing.T) {
	_, err := executeUpdateBusinessProfile(context.Background(), json.RawMessage(`{"brand_name":"a"}`), Scope{Queries: &sqlc.Queries{}, ProjectID: testProjectID, UserID: testUserID})
	if err == nil || !strings.Contains(err.Error(), "transaction support") {
		t.Fatalf("missing DB should fail infra, got %v", err)
	}
	_, err = executeUpdateBusinessProfile(context.Background(), json.RawMessage(`{"brand_name":"a"}`), Scope{DB: &fakeTransactor{}, ProjectID: testProjectID, UserID: testUserID})
	if err == nil || !strings.Contains(err.Error(), "no queries") {
		t.Fatalf("missing queries should fail, got %v", err)
	}
}

func TestUpdateBusinessProfileNonOwnerDenied(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:      testProjectID,
		BrandName:      "Old",
		WebsiteUrl:     "https://old.example",
		SeedPrompts:    []byte(`[]`),
		TargetKeywords: []byte(`[]`),
	}
	store := newFakeUpdateStore("member", profile)
	_, err := runPatch(t, store, `{"brand_name":"New"}`)
	if err == nil || !strings.Contains(err.Error(), "only organization owners") {
		t.Fatalf("expected owner denial model error, got %v", err)
	}
	if !isModelError(err) {
		t.Fatalf("should be model error")
	}
}

func TestUpdateBusinessProfileMissingCreationRequirements(t *testing.T) {
	store := newFakeUpdateStore("owner", nil)
	_, err := runPatch(t, store, `{"brand_name":"Acme"}`)
	if err == nil || !strings.Contains(err.Error(), "provide non-empty brand_name and website_url") {
		t.Fatalf("missing profile error = %v, want creation requirement", err)
	}
	_, err = runPatch(t, store, `{"website_url":"https://acme.example"}`)
	if err == nil || !strings.Contains(err.Error(), "provide non-empty brand_name and website_url") {
		t.Fatalf("missing website error = %v", err)
	}
	// success
	res, err := runPatch(t, store, `{"brand_name":"Acme","website_url":"https://acme.example"}`)
	if err != nil {
		t.Fatalf("creation failed: %v", err)
	}
	m := parseUpdateResult(t, res)
	if m["brand_name"] != "Acme" {
		t.Fatalf("created %v", m)
	}
}

func TestUpdateBusinessProfileOmittedFieldPreservation(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:           testProjectID,
		BrandName:           "Acme",
		WebsiteUrl:          "https://acme.example",
		PrimaryCategory:     pgtype.Text{String: "E-commerce", Valid: true},
		PrimaryLocation:     pgtype.Text{String: "Portland", Valid: true},
		BusinessDescription: pgtype.Text{String: "Sells gear", Valid: true},
		SeedPrompts:         []byte(`["prompt one"]`),
		TargetKeywords:      []byte(`["seo","maps"]`),
	}
	store := newFakeUpdateStore("owner", profile)
	res, err := runPatch(t, store, `{"brand_name":"NewAcme"}`)
	if err != nil {
		t.Fatalf("update error %v", err)
	}
	m := parseUpdateResult(t, res)
	if m["brand_name"] != "NewAcme" || m["primary_category"] != "E-commerce" || m["primary_location"] != "Portland" {
		t.Fatalf("omitted not preserved: %v", m)
	}
	if !strings.Contains(res.Summary, "brand_name") {
		t.Fatalf("Summary %q", res.Summary)
	}
	// check db preserved
	if store.profile.PrimaryCategory.String != "E-commerce" {
		t.Fatalf("db not preserved")
	}
}

func TestUpdateBusinessProfileClearingWithEmptyArray(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:      testProjectID,
		BrandName:      "Acme",
		WebsiteUrl:     "https://acme.example",
		SeedPrompts:    []byte(`["a","b"]`),
		TargetKeywords: []byte(`["seo","maps"]`),
	}
	store := newFakeUpdateStore("owner", profile)
	res, err := runPatch(t, store, `{"seed_prompts":[],"target_keywords":[]}`)
	if err != nil {
		t.Fatalf("clear %v", err)
	}
	m := parseUpdateResult(t, res)
	if seed, _ := m["seed_prompts"].([]interface{}); len(seed) != 0 {
		t.Fatalf("seed clear %v", seed)
	}
}

func TestUpdateBusinessProfileKeywordNormalization(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:      testProjectID,
		BrandName:      "Acme",
		WebsiteUrl:     "https://acme.example",
		TargetKeywords: []byte(`[]`),
		SeedPrompts:    []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	res, err := runPatch(t, store, `{"target_keywords":["  SEO  ","seo","Seo","  Maps ","","  ","maps"," Go "]}`)
	if err != nil {
		t.Fatalf("%v", err)
	}
	m := parseUpdateResult(t, res)
	kw, _ := m["target_keywords"].([]interface{})
	if len(kw) != 3 || kw[0] != "SEO" || kw[1] != "Maps" {
		t.Fatalf("kw %v", kw)
	}
}

func TestUpdateBusinessProfileKeywordReplacement(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:      testProjectID,
		BrandName:      "Acme",
		WebsiteUrl:     "https://acme.example",
		TargetKeywords: []byte(`["old1","old2"]`),
		SeedPrompts:    []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	res, err := runPatch(t, store, `{"target_keywords":["new1","new2"]}`)
	if err != nil {
		t.Fatalf("%v", err)
	}
	m := parseUpdateResult(t, res)
	kw, _ := m["target_keywords"].([]interface{})
	if len(kw) != 2 || kw[0] != "new1" {
		t.Fatalf("%v", kw)
	}
}

func TestUpdateBusinessProfileSeedValidation(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`), TargetKeywords: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	_, err := runPatch(t, store, `{"seed_prompts":["a","b","c","d","e","f"]}`)
	if err == nil || !strings.Contains(err.Error(), "cannot contain more than 5") {
		t.Fatalf("expected max %v", err)
	}
	_, err = runPatch(t, store, `{"seed_prompts":["  "]}`)
	if err == nil || !strings.Contains(err.Error(), "cannot contain empty prompts") {
		t.Fatalf("expected empty %v", err)
	}
}

func TestUpdateBusinessProfileBrandCannotBeCleared(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`), TargetKeywords: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	_, err := runPatch(t, store, `{"brand_name":"   "}`)
	if err == nil || !strings.Contains(err.Error(), "brand_name cannot be empty") {
		t.Fatalf("%v", err)
	}
}

func TestUpdateBusinessProfileRegistryAndCatalog(t *testing.T) {
	reg := NewRegistry()
	if _, ok := reg.Get("update_business_profile"); !ok {
		t.Fatalf("registry missing")
	}
	found := false
	for _, def := range CatalogDefs() {
		if def.Name == "update_business_profile" {
			found = true
			var schema map[string]interface{}
			if err := json.Unmarshal(def.Schema, &schema); err != nil {
				t.Fatalf("%v", err)
			}
			props, _ := schema["properties"].(map[string]interface{})
			if _, ok := props["brand_name"]; !ok {
				t.Fatal("missing brand_name")
			}
			if _, ok := props["user_id"]; ok {
				t.Fatal("must not contain user_id")
			}
			if !strings.Contains(def.Description, "only after the user clearly asks") {
				t.Fatalf("description should state user-request gate, got %q", def.Description)
			}
		}
	}
	if !found {
		t.Fatal("catalog missing")
	}
}

func TestUpdateBusinessProfileCorruptStoredJSON(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:      testProjectID,
		BrandName:      "Acme",
		WebsiteUrl:     "https://acme.example",
		SeedPrompts:    []byte(`not json`),
		TargetKeywords: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	// omitted seed should trigger infra error
	_, err := runPatch(t, store, `{"brand_name":"New"}`)
	if err == nil || !strings.Contains(err.Error(), "decode seed_prompts") {
		t.Fatalf("expected infra decode error, got %v", err)
	}
	if isModelError(err) {
		t.Fatalf("corrupt should be infra, not model")
	}
	// provided seed should still error on existing corrupt? Our patch now errors even when provided (since we decode existing for diff). That's infra too.
}

func TestBusinessProfileTargetKeywordsAlwaysReturned(t *testing.T) {
	fake := &fakeBusinessProfileReader{profile: sqlc.GetProjectBusinessProfileByProjectIDForUserRow{
		BrandName: "Acme", WebsiteUrl: "https://acme.example",
		TargetKeywords: []byte(`[]`), SeedPrompts: []byte(`[]`),
	}}
	result := runBusinessProfile(t, fake, `{}`)
	var resp businessProfileResponse
	if err := json.Unmarshal([]byte(result.Content), &resp); err != nil {
		t.Fatalf("%v", err)
	}
	if resp.TargetKeywords == nil {
		t.Fatalf("nil")
	}
	fake.profile.TargetKeywords = []byte(`["a","b"]`)
	result = runBusinessProfile(t, fake, `{}`)
	if err := json.Unmarshal([]byte(result.Content), &resp); err != nil {
		t.Fatalf("%v", err)
	}
	if len(resp.TargetKeywords) != 2 {
		t.Fatalf("%v", resp.TargetKeywords)
	}
}
