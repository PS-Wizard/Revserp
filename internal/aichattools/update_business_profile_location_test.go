package aichattools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// fakeUpdateLocationStore implements updateBusinessProfileLocationQuerier
// without a database. parentWrites counts parent-table writes, which must
// stay zero: the local branch never touches the parent profile.
type fakeUpdateLocationStore struct {
	location     sqlc.GetProjectLocationForUserRow
	locErr       error
	role         string
	memberErr    error
	profile      sqlc.LocationBusinessProfile
	profErr      error
	upserted     sqlc.UpsertLocationBusinessProfileParams
	upsertErr    error
	parentWrites int
}

func (f *fakeUpdateLocationStore) GetProjectLocationForUser(_ context.Context, _ sqlc.GetProjectLocationForUserParams) (sqlc.GetProjectLocationForUserRow, error) {
	return f.location, f.locErr
}

func (f *fakeUpdateLocationStore) GetOrganizationMember(_ context.Context, _ sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error) {
	if f.memberErr != nil {
		return sqlc.OrganizationMember{}, f.memberErr
	}
	return sqlc.OrganizationMember{Role: f.role}, nil
}

func (f *fakeUpdateLocationStore) GetLocationBusinessProfile(_ context.Context, _ sqlc.GetLocationBusinessProfileParams) (sqlc.LocationBusinessProfile, error) {
	return f.profile, f.profErr
}

func (f *fakeUpdateLocationStore) UpsertLocationBusinessProfile(_ context.Context, arg sqlc.UpsertLocationBusinessProfileParams) (sqlc.LocationBusinessProfile, error) {
	if f.upsertErr != nil {
		return sqlc.LocationBusinessProfile{}, f.upsertErr
	}
	f.upserted = arg
	return sqlc.LocationBusinessProfile{
		BrandName:           arg.BrandName,
		WebsiteUrl:          arg.WebsiteUrl,
		PrimaryCategory:     arg.PrimaryCategory,
		PrimaryLocation:     arg.PrimaryLocation,
		BusinessDescription: arg.BusinessDescription,
		ProductDescription:  arg.ProductDescription,
		TargetAudience:      arg.TargetAudience,
		BusinessCompetitors: arg.BusinessCompetitors,
		SeedPrompts:         arg.SeedPrompts,
		Services:            []byte(`["Catering"]`),
	}, nil
}

func ownerLocationStore() *fakeUpdateLocationStore {
	return &fakeUpdateLocationStore{
		location: sqlc.GetProjectLocationForUserRow{OrganizationID: testProjectID},
		role:     "owner",
		profile:  makeLocationProfile(),
	}
}

func runLocalPatch(t *testing.T, store *fakeUpdateLocationStore, raw string) (Result, []string, bool) {
	t.Helper()
	exec := updateBusinessProfileLocalExecutor{locations: store}
	args, err := parseUpdateBusinessProfileLocalArgs(json.RawMessage(raw))
	if err != nil {
		return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil, false
	}
	res, changed, existed, err := exec.patchLocal(context.Background(), args, testProjectID, testLocationID, testUserID, store)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil, false
		}
		t.Fatalf("patchLocal(%s) returned error: %v", raw, err)
	}
	return res, changed, existed
}

func TestUpdateLocalBusinessProfileOwnerOnly(t *testing.T) {
	store := ownerLocationStore()
	store.role = "member"
	result, _, _ := runLocalPatch(t, store, `{"brand_name":"New"}`)
	if !strings.Contains(result.Content, "only organization owners") {
		t.Fatalf("non-owner must be denied, got: %s", result.Content)
	}
	store = ownerLocationStore()
	store.locErr = pgx.ErrNoRows
	result, _, _ = runLocalPatch(t, store, `{"brand_name":"New"}`)
	if !strings.Contains(result.Content, "location not found or access denied") {
		t.Fatalf("foreign location must be denied, got: %s", result.Content)
	}
}

func TestUpdateLocalBusinessProfileCreateRequiresBrandAndSite(t *testing.T) {
	store := ownerLocationStore()
	store.profErr = pgx.ErrNoRows
	result, _, _ := runLocalPatch(t, store, `{"brand_name":"Only brand"}`)
	if !strings.Contains(result.Content, "provide non-empty brand_name and website_url") {
		t.Fatalf("creation must require both, got: %s", result.Content)
	}
}

func TestUpdateLocalBusinessProfilePreservesServicesWhenOmitted(t *testing.T) {
	store := ownerLocationStore()
	result, changed, existed := runLocalPatch(t, store, `{"primary_category":"Bakery"}`)
	if !existed {
		t.Fatalf("existing profile must read as existed")
	}
	if len(changed) != 1 || changed[0] != "primary_category" {
		t.Fatalf("wrong changed set: %v", changed)
	}
	if store.upserted.Column12 != nil {
		t.Fatalf("omitted services must preserve the snapshot, got column: %v", store.upserted.Column12)
	}
	if store.parentWrites != 0 {
		t.Fatalf("local branch must never write parent tables")
	}
	parsed := parseUpdateResult(t, result)
	services, ok := parsed["services"].([]interface{})
	if !ok || len(services) != 2 || services[0] != "Catering" || services[1] != "Repairs" {
		t.Fatalf("response must carry stored services: %v", parsed)
	}
	if parsed["location_id"] != testLocationID.String() {
		t.Fatalf("response must carry location_id: %v", parsed)
	}
}

func TestUpdateLocalBusinessProfileReplacesServicesWhenProvided(t *testing.T) {
	store := ownerLocationStore()
	_, changed, _ := runLocalPatch(t, store, `{"services":["  catering ","NEW SERVICE","Catering"]}`)
	found := false
	for _, field := range changed {
		if field == "services" {
			found = true
		}
	}
	if !found {
		t.Fatalf("services change must be tracked, got: %v", changed)
	}
	var stored []string
	if err := json.Unmarshal(store.upserted.Column12.([]byte), &stored); err != nil {
		t.Fatalf("services column must be JSON: %v", err)
	}
	if len(stored) != 2 || stored[0] != "catering" || stored[1] != "NEW SERVICE" {
		t.Fatalf("services must dedupe preserving first spelling: %v", stored)
	}
}

func TestUpdateLocalBusinessProfileRejectsProjectKeywords(t *testing.T) {
	for _, raw := range []string{`{"branded_keywords":["a"]}`, `{"non_branded_keywords":["a"]}`, `{"target_keywords":["a"]}`} {
		_, err := parseUpdateBusinessProfileLocalArgs(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "location keyword lists") {
			t.Fatalf("raw %s must route to keyword lists, got %v", raw, err)
		}
	}
	_, err := parseUpdateBusinessProfileLocalArgs(json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "no fields provided") {
		t.Fatalf("empty patch must fail, got %v", err)
	}
}

func TestNormalizeLocationServices(t *testing.T) {
	got, err := normalizeLocationServices([]string{"  Catering ", "catering", "Repairs"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != 2 || got[0] != "Catering" || got[1] != "Repairs" {
		t.Fatalf("dedupe failed: %v", got)
	}
	if _, err := normalizeLocationServices([]string{"a\x00b"}); err == nil {
		t.Fatalf("nul label must fail")
	}
}

func TestUpdateLocalBusinessProfilePreservesProductDescriptionVerbatim(t *testing.T) {
	exact := "  Stone-baked\nloaf,\n400g\n  "
	rawArgs, err := json.Marshal(map[string]string{"product_description": exact})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	store := ownerLocationStore()
	result, changed, existed := runLocalPatch(t, store, string(rawArgs))
	if !existed {
		t.Fatalf("existing profile must read as existed")
	}
	found := false
	for _, field := range changed {
		if field == "product_description" {
			found = true
		}
	}
	if !found {
		t.Fatalf("verbatim change must be tracked, got: %v", changed)
	}
	if !store.upserted.ProductDescription.Valid || store.upserted.ProductDescription.String != exact {
		t.Fatalf("stored bytes must stay exact, got: %q", store.upserted.ProductDescription.String)
	}
	parsed := parseUpdateResult(t, result)
	if parsed["product_description"] != exact {
		t.Fatalf("response bytes must stay exact, got: %q", parsed["product_description"])
	}
}

func TestUpdateLocalBusinessProfileCreatePreservesProductDescriptionVerbatim(t *testing.T) {
	exact := "  Stone-baked\nloaf,\n400g\n  "
	rawArgs, err := json.Marshal(map[string]string{
		"brand_name":          "Acme",
		"website_url":         "https://acme.example",
		"product_description": exact,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	store := ownerLocationStore()
	store.profErr = pgx.ErrNoRows
	_, _, existed := runLocalPatch(t, store, string(rawArgs))
	if existed {
		t.Fatalf("missing profile must read as creation")
	}
	if !store.upserted.ProductDescription.Valid || store.upserted.ProductDescription.String != exact {
		t.Fatalf("created bytes must stay exact, got: %q", store.upserted.ProductDescription.String)
	}
}
