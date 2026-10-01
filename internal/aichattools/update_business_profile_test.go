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
	// The profile upsert carries no keyword columns: suggested keywords persist
	// through the keyword service, and the patch never reads keyword bytes off
	// the upserted row (they become read-only combined aliases).
	row := sqlc.GetProjectBusinessProfileByProjectIDRow{
		ID:                  pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
		ProjectID:           arg.ProjectID,
		BrandName:           arg.BrandName,
		WebsiteUrl:          arg.WebsiteUrl,
		PrimaryCategory:     arg.PrimaryCategory,
		PrimaryLocation:     arg.PrimaryLocation,
		BusinessDescription: arg.BusinessDescription,
		ProductDescription:  arg.ProductDescription,
		TargetAudience:      arg.TargetAudience,
		BusinessCompetitors: arg.BusinessCompetitors,
		SeedPrompts:         arg.SeedPrompts,
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
		ProductDescription:  row.ProductDescription,
		TargetAudience:      row.TargetAudience,
		BusinessCompetitors: row.BusinessCompetitors,
		SeedPrompts:         row.SeedPrompts,
	}, nil
}

func (f *fakeUpdateStore) EnqueueAIWorkerJob(_ context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error) {
	f.enqueued++
	return sqlc.EnqueueAIWorkerJobRow{JobType: arg.JobType, ProjectID: arg.ProjectID}, nil
}

// helper that tests pure patch logic via fake querier without transaction.
func runPatch(t *testing.T, store *fakeUpdateStore, raw string) (Result, error) {
	t.Helper()
	res, _, _, err := runPatchOutcome(t, store, newFakeProjectKeywordService(), raw)
	return res, err
}

// runPatchWithKeywords tests patch logic against a seeded keyword service.
func runPatchWithKeywords(t *testing.T, store *fakeUpdateStore, svc *fakeProjectKeywordService, raw string) (Result, error) {
	t.Helper()
	res, _, _, err := runPatchOutcome(t, store, svc, raw)
	return res, err
}

func runPatchOutcome(t *testing.T, store *fakeUpdateStore, svc *fakeProjectKeywordService, raw string) (Result, []string, bool, error) {
	t.Helper()
	exec := updateBusinessProfileExecutor{queries: &sqlc.Queries{}, db: &fakeTransactor{}, keywords: svc}
	args, err := parseUpdateBusinessProfileArgs(json.RawMessage(raw))
	if err != nil {
		return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil, false, nil
	}
	res, changed, existed, err := exec.patch(context.Background(), args, testProjectID, testUserID, store, &sqlc.Queries{})
	return res, changed, existed, err
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
	if err == nil || !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("target keywords null should be rejected as stale, got %v", err)
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
	// Brand and site alone no longer create: both keyword lists are required.
	_, err = runPatch(t, store, `{"brand_name":"Acme","website_url":"https://acme.example"}`)
	if err == nil || !strings.Contains(err.Error(), "branded_keywords and non_branded_keywords") {
		t.Fatalf("keywordless create error = %v, want both-lists requirement", err)
	}
	// success with both complete lists
	svc := newFakeProjectKeywordService()
	res, err := runPatchWithKeywords(t, store, svc, `{"brand_name":"Acme","website_url":"https://acme.example","branded_keywords":["Acme"],"non_branded_keywords":["trail widgets"]}`)
	if err != nil {
		t.Fatalf("creation failed: %v", err)
	}
	m := parseUpdateResult(t, res)
	if m["brand_name"] != "Acme" {
		t.Fatalf("created %v", m)
	}
	if svc.replaceCalls != 1 {
		t.Fatalf("create replace calls = %d, want atomic keyword persist", svc.replaceCalls)
	}
	if len(svc.lastReplaceBrand) != 1 || len(svc.lastReplaceNonBrand) != 1 {
		t.Fatalf("create replace = %v / %v, want both lists persisted", svc.lastReplaceBrand, svc.lastReplaceNonBrand)
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
	res, err := runPatch(t, store, `{"seed_prompts":[]}`)
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
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	svc := newFakeProjectKeywordService()
	res, err := runPatchWithKeywords(t, store, svc, `{"branded_keywords":["  Acme  ","acme","ACME"],"non_branded_keywords":["  Maps ","","  ","maps"," Go "]}`)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(svc.lastReplaceBrand) != 1 || svc.lastReplaceBrand[0] != "Acme" {
		t.Fatalf("brand replace = %v, want deduped [Acme]", svc.lastReplaceBrand)
	}
	if len(svc.lastReplaceNonBrand) != 2 || svc.lastReplaceNonBrand[0] != "Maps" {
		t.Fatalf("non-brand replace = %v, want [Maps Go]", svc.lastReplaceNonBrand)
	}
	m := parseUpdateResult(t, res)
	branded, _ := m["branded_keywords"].([]interface{})
	if len(branded) != 1 || branded[0] != "Acme" {
		t.Fatalf("branded %v", branded)
	}
}

func TestUpdateBusinessProfileKeywordReplacement(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	svc := newFakeProjectKeywordService().withRevserp([]string{"old1", "old2"}, []string{"old3"})
	res, err := runPatchWithKeywords(t, store, svc, `{"branded_keywords":["new1","new2"],"non_branded_keywords":["new3"]}`)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if svc.replaceCalls != 1 {
		t.Fatalf("replace calls = %d, want 1", svc.replaceCalls)
	}
	m := parseUpdateResult(t, res)
	branded, _ := m["branded_keywords"].([]interface{})
	if len(branded) != 2 || branded[0] != "new1" {
		t.Fatalf("%v", branded)
	}
	if _, ok := m["target_keywords"]; ok {
		t.Fatalf("response %v must not carry target_keywords", m)
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

func TestUpdateBusinessProfileRejectsStaleTargetKeywords(t *testing.T) {
	for _, raw := range []string{
		`{"brand_name":"Acme","target_keywords":["seo"]}`,
		`{"target_keywords":[]}`,
		`{"target_keywords":null}`,
	} {
		_, err := parseUpdateBusinessProfileArgs(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "no longer supported") || !strings.Contains(err.Error(), "update_project_keywords") {
			t.Fatalf("raw %s should be rejected as stale with keyword-tool guidance, got %v", raw, err)
		}
	}
}

func TestShouldEnqueuePromptGenerationAfterProfileWrite(t *testing.T) {
	cases := []struct {
		name    string
		exists  bool
		changed []string
		want    bool
	}{
		{"creation always enqueues", false, []string{"brand_name"}, true},
		{"creation with keywords enqueues", false, []string{"brand_name", "branded_keywords", "non_branded_keywords"}, true},
		{"profile change enqueues", true, []string{"brand_name"}, true},
		{"mixed keyword and profile change enqueues", true, []string{"branded_keywords", "non_branded_keywords", "target_audience"}, true},
		{"keyword-only edit never enqueues", true, []string{"branded_keywords", "non_branded_keywords"}, false},
		{"single suggested side never enqueues", true, []string{"non_branded_keywords"}, false},
		{"no-change save enqueues nothing", true, nil, false},
		{"empty changed slice enqueues nothing", true, []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldEnqueuePromptGenerationAfterProfileWrite(tc.exists, tc.changed); got != tc.want {
				t.Fatalf("shouldEnqueue(exists=%v, changed=%v) = %v, want %v", tc.exists, tc.changed, got, tc.want)
			}
		})
	}
}

func TestUpdateBusinessProfileKeywordWritePreservesUserSource(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	svc := newFakeProjectKeywordService().
		withUser([]string{"Acme"}, []string{"user kept term"}).
		withRevserp([]string{"Old"}, []string{"old term"})
	res, err := runPatchWithKeywords(t, store, svc, `{"branded_keywords":["New Brand"],"non_branded_keywords":["new need"]}`)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if svc.replaceCalls != 1 {
		t.Fatalf("replace calls = %d, want 1", svc.replaceCalls)
	}
	lists, err := svc.LoadProjectKeywordLists(context.Background(), &sqlc.Queries{}, testProjectID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(lists.UserDefined) != 2 {
		t.Fatalf("user-defined = %+v, want both user entries preserved", lists.UserDefined)
	}
	for _, entry := range lists.RevserpSuggested {
		for _, user := range []string{"Acme", "user kept term"} {
			if entry.Keyword == user {
				t.Fatalf("suggested entry %q overlaps a user term; user text must not leak into the suggested source", entry.Keyword)
			}
		}
	}
	m := parseUpdateResult(t, res)
	if _, ok := m["target_keywords"]; ok {
		t.Fatalf("response %v must not carry target_keywords", m)
	}
	branded, _ := m["branded_keywords"].([]interface{})
	if len(branded) != 1 || branded[0] != "New Brand" {
		t.Fatalf("branded = %v, want stored suggested list", branded)
	}
}

func TestUpdateBusinessProfileIdenticalKeywordsSkipReplace(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	svc := newFakeProjectKeywordService().withRevserp([]string{"Acme"}, []string{"trail widgets"})
	_, changed, _, err := runPatchOutcome(t, store, svc, `{"branded_keywords":["acme"],"non_branded_keywords":["trail widgets"]}`)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if svc.replaceCalls != 0 {
		t.Fatalf("identical keyword sets must skip the replace, calls = %d", svc.replaceCalls)
	}
	for _, field := range changed {
		if field == "branded_keywords" || field == "non_branded_keywords" {
			t.Fatalf("changed = %v, want no keyword churn for identical sets", changed)
		}
	}
}

func TestUpdateBusinessProfileRepeatedKeywordsDoNotEnqueue(t *testing.T) {
	profile := &sqlc.GetProjectBusinessProfileByProjectIDRow{
		ProjectID:   testProjectID,
		BrandName:   "Acme",
		WebsiteUrl:  "https://acme.example",
		SeedPrompts: []byte(`[]`),
	}
	store := newFakeUpdateStore("owner", profile)
	svc := newFakeProjectKeywordService().withRevserp([]string{"Acme"}, []string{"trail widgets"})
	for i := 0; i < 2; i++ {
		_, changed, existed, err := runPatchOutcome(t, store, svc, `{"branded_keywords":["Acme"],"non_branded_keywords":["trail widgets"]}`)
		if err != nil {
			t.Fatalf("repeat %d patch: %v", i, err)
		}
		if !existed {
			t.Fatal("existed = false, want existing profile")
		}
		if len(changed) != 0 {
			t.Fatalf("repeat %d changed = %v, want no actual changes", i, changed)
		}
		if shouldEnqueuePromptGenerationAfterProfileWrite(existed, changed) {
			t.Fatalf("repeat %d should not enqueue prompt_generation for identical keywords", i)
		}
	}
	if svc.replaceCalls != 0 {
		t.Fatalf("replace calls = %d, want no suggested rewrite for identical keywords", svc.replaceCalls)
	}
}
