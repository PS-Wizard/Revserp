package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

type fakeProjectKeywordService struct {
	userBrand, userNonBrand       []string
	revserpBrand, revserpNonBrand []string
	replaceCalls                  int
	lastReplaceBrand              []string
	lastReplaceNonBrand           []string
	loadErr                       error
	replaceErr                    error
}

func newFakeProjectKeywordService() *fakeProjectKeywordService {
	return &fakeProjectKeywordService{}
}

func (f *fakeProjectKeywordService) withRevserp(brand, nonBrand []string) *fakeProjectKeywordService {
	f.revserpBrand = append([]string{}, brand...)
	f.revserpNonBrand = append([]string{}, nonBrand...)
	return f
}

func (f *fakeProjectKeywordService) withUser(brand, nonBrand []string) *fakeProjectKeywordService {
	f.userBrand = append([]string{}, brand...)
	f.userNonBrand = append([]string{}, nonBrand...)
	return f
}

func (f *fakeProjectKeywordService) LoadProjectKeywordLists(_ context.Context, _ *sqlc.Queries, _ pgtype.UUID) (projectkeywords.KeywordLists, error) {
	if f.loadErr != nil {
		return projectkeywords.KeywordLists{}, f.loadErr
	}
	rows := make([]projectkeywords.StoredProjectKeyword, 0)
	add := func(phrases []string, kind, source string) {
		for i, phrase := range phrases {
			rows = append(rows, projectkeywords.StoredProjectKeyword{
				ID:         fmt.Sprintf("%s-%s-%d", source, kind, i),
				Keyword:    phrase,
				Normalized: projectkeywords.NormalizeProjectKeywordKey(phrase),
				Kind:       kind,
				Source:     source,
			})
		}
	}
	add(f.userBrand, projectkeywords.ProjectKeywordKindBrand, projectkeywords.ProjectKeywordSourceUser)
	add(f.userNonBrand, projectkeywords.ProjectKeywordKindNonBrand, projectkeywords.ProjectKeywordSourceUser)
	add(f.revserpBrand, projectkeywords.ProjectKeywordKindBrand, projectkeywords.ProjectKeywordSourceRevserp)
	add(f.revserpNonBrand, projectkeywords.ProjectKeywordKindNonBrand, projectkeywords.ProjectKeywordSourceRevserp)
	return projectkeywords.BuildProjectKeywordLists(rows), nil
}

func (f *fakeProjectKeywordService) ReplaceSuggestedKeywords(_ context.Context, _ *sqlc.Queries, _ pgtype.UUID, brandKeywords, nonBrandKeywords []string) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.replaceCalls++
	f.lastReplaceBrand = append([]string{}, brandKeywords...)
	f.lastReplaceNonBrand = append([]string{}, nonBrandKeywords...)
	f.revserpBrand = append([]string{}, brandKeywords...)
	f.revserpNonBrand = append([]string{}, nonBrandKeywords...)
	return nil
}

type fakeProjectKeywordAccess struct {
	project sqlc.Project
	err     error
}

func (f *fakeProjectKeywordAccess) GetProjectByIDForUser(_ context.Context, arg sqlc.GetProjectByIDForUserParams) (sqlc.Project, error) {
	if f.err != nil {
		return sqlc.Project{}, f.err
	}
	if arg.ID != f.project.ID || arg.UserID != testUserID {
		return sqlc.Project{}, pgx.ErrNoRows
	}
	return f.project, nil
}

type fakeSQLExec struct {
	query string
	args  []interface{}
	err   error
	calls int
}

func (f *fakeSQLExec) Exec(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	f.query = sql
	f.args = args
	f.calls++
	if f.err != nil {
		return pgconn.CommandTag{}, f.err
	}
	return pgconn.CommandTag{}, nil
}

func runGetProjectKeywords(t *testing.T, access *fakeProjectKeywordAccess, svc *fakeProjectKeywordService, raw string) (Result, error) {
	t.Helper()
	exec := getProjectKeywordsExecutor{access: access, keywords: svc}
	return exec.run(context.Background(), json.RawMessage(raw), testProjectID, testUserID, &sqlc.Queries{})
}

func runApplyProjectKeywords(t *testing.T, store *fakeUpdateStore, svc *fakeProjectKeywordService, execSQL *fakeSQLExec, raw string) (Result, error) {
	t.Helper()
	args, err := parseUpdateProjectKeywordsArgs(json.RawMessage(raw))
	if err != nil {
		return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
	}
	exec := updateProjectKeywordsExecutor{queries: &sqlc.Queries{}, keywords: svc, emitEvent: func(_ context.Context, _ sqlExecer, orgID, projectID pgtype.UUID, brandCount, nonBrandCount int) error {
		return emitProjectKeywordsUpdated(context.Background(), execSQL, orgID, projectID, brandCount, nonBrandCount)
	}}
	return exec.apply(context.Background(), args, testProjectID, testUserID, store, &sqlc.Queries{}, execSQL)
}

func TestGetProjectKeywordsRejectsAllArguments(t *testing.T) {
	access := &fakeProjectKeywordAccess{project: sqlc.Project{ID: testProjectID}}
	svc := newFakeProjectKeywordService()
	for _, raw := range []string{`{"project_id":"x"}`, `{"user_id":"x"}`, `{"include_seed_prompts":true}`} {
		result, err := runGetProjectKeywords(t, access, svc, raw)
		if err != nil {
			t.Fatalf("raw %s: unexpected infra error %v", raw, err)
		}
		if !strings.Contains(result.Content, "unknown argument") {
			t.Fatalf("raw %s should be rejected, got %q", raw, result.Content)
		}
	}
}

func TestGetProjectKeywordsAccessDenied(t *testing.T) {
	access := &fakeProjectKeywordAccess{project: sqlc.Project{ID: testProjectID}}
	svc := newFakeProjectKeywordService()
	result, err := runGetProjectKeywords(t, access, svc, `{}`)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	_ = result
	other := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	access = &fakeProjectKeywordAccess{project: sqlc.Project{ID: other}}
	result, err = runGetProjectKeywords(t, access, svc, `{}`)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(result.Content, "project not found or access denied") {
		t.Fatalf("expected access denial, got %q", result.Content)
	}
}

func TestGetProjectKeywordsReturnsAllListsWithBadges(t *testing.T) {
	access := &fakeProjectKeywordAccess{project: sqlc.Project{ID: testProjectID}}
	svc := newFakeProjectKeywordService().
		withUser([]string{"Acme"}, []string{"trail widgets"}).
		withRevserp([]string{"Acme Pro"}, []string{"trail widgets", "hiking gear"})
	result, err := runGetProjectKeywords(t, access, svc, `{}`)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	var lists projectkeywords.KeywordLists
	if err := json.Unmarshal([]byte(result.Content), &lists); err != nil {
		t.Fatalf("content is not keyword lists JSON: %v\n%s", err, result.Content)
	}
	if len(lists.UserDefined) != 2 || len(lists.RevserpSuggested) != 3 {
		t.Fatalf("lists = %+v, want 2 user + 3 suggested", lists)
	}
	byText := map[string]projectkeywords.CombinedKeyword{}
	for _, entry := range lists.Combined {
		byText[entry.Keyword] = entry
	}
	overlap, ok := byText["trail widgets"]
	if !ok || len(overlap.Sources) != 2 {
		t.Fatalf("overlap entry = %+v, want both source badges", overlap)
	}
	if overlap.Kind != projectkeywords.ProjectKeywordKindNonBrand {
		t.Fatalf("overlap kind = %q, want non_brand", overlap.Kind)
	}
	if !strings.Contains(result.Summary, "2 user-defined") || !strings.Contains(result.Summary, "3 suggested") {
		t.Fatalf("summary = %q, want counts", result.Summary)
	}
}

func TestNonNullProjectKeywordLists(t *testing.T) {
	lists := nonNullProjectKeywordLists(projectkeywords.KeywordLists{})
	raw, _ := json.Marshal(lists)
	if string(raw) != `{"user_defined":[],"revserp_suggested":[],"combined":[]}` {
		t.Fatalf("empty marshal = %s, want non-null arrays", raw)
	}
}

func TestUpdateProjectKeywordsParseRequiresBothLists(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"brand_keywords":["a"]}`,
		`{"non_brand_keywords":["b"]}`,
		`{"brand_keywords":[],"non_brand_keywords":["b"]}`,
		`{"brand_keywords":["  "],"non_brand_keywords":["b"]}`,
		`{"brand_keywords":["a"],"non_brand_keywords":[]}`,
		`{"brand_keywords":null,"non_brand_keywords":["b"]}`,
		`{"brand_keywords":"a","non_brand_keywords":["b"]}`,
		`{"brand_keywords":["a"],"non_brand_keywords":["b"],"source":"user"}`,
		`{"brand_keywords":["a"],"non_brand_keywords":["b"],"project_id":"x"}`,
	} {
		if _, err := parseUpdateProjectKeywordsArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("raw %s should be rejected", raw)
		}
	}
	args, err := parseUpdateProjectKeywordsArgs(json.RawMessage(`{"brand_keywords":[" Acme "],"non_brand_keywords":["gear"]}`))
	if err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	if len(args.BrandKeywords) != 1 || len(args.NonBrandKeywords) != 1 {
		t.Fatalf("args = %+v", args)
	}
}

func TestUpdateProjectKeywordsNonOwnerDenied(t *testing.T) {
	store := newFakeUpdateStore("member", nil)
	svc := newFakeProjectKeywordService()
	_, err := runApplyProjectKeywords(t, store, svc, &fakeSQLExec{}, `{"brand_keywords":["a"],"non_brand_keywords":["b"]}`)
	if err == nil || !strings.Contains(err.Error(), "only organization owners") {
		t.Fatalf("member should be denied, got %v", err)
	}
	if !isModelError(err) {
		t.Fatal("denial should be a model error")
	}
	if svc.replaceCalls != 0 {
		t.Fatal("denied call must not write suggested keywords")
	}
}

func TestUpdateProjectKeywordsSuccessReplacesRevserpOnly(t *testing.T) {
	store := newFakeUpdateStore("owner", nil)
	svc := newFakeProjectKeywordService().
		withUser([]string{"Acme"}, []string{"user term"}).
		withRevserp([]string{"Old Brand"}, []string{"old term"})
	execSQL := &fakeSQLExec{}
	result, err := runApplyProjectKeywords(t, store, svc, execSQL, `{"brand_keywords":["  Acme Pro  ","acme pro"],"non_brand_keywords":["Hiking Gear","hiking gear "]}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if svc.replaceCalls != 1 {
		t.Fatalf("replace calls = %d, want 1", svc.replaceCalls)
	}
	if len(svc.lastReplaceBrand) != 1 || svc.lastReplaceBrand[0] != "Acme Pro" {
		t.Fatalf("brand replace = %v, want deduped trimmed [Acme Pro]", svc.lastReplaceBrand)
	}
	if len(svc.lastReplaceNonBrand) != 1 || svc.lastReplaceNonBrand[0] != "Hiking Gear" {
		t.Fatalf("non-brand replace = %v", svc.lastReplaceNonBrand)
	}
	lists, err := svc.LoadProjectKeywordLists(context.Background(), &sqlc.Queries{}, testProjectID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(lists.UserDefined) != 2 {
		t.Fatalf("user-defined = %+v, want both entries preserved", lists.UserDefined)
	}
	if execSQL.calls != 1 || !strings.Contains(execSQL.query, "project_keywords.updated") {
		t.Fatalf("event insert = %q x%d, want one project_keywords.updated", execSQL.query, execSQL.calls)
	}
	if len(execSQL.args) != 3 {
		t.Fatalf("event args = %v, want org, project, payload", execSQL.args)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(execSQL.args[2].(string)), &payload); err != nil {
		t.Fatalf("event payload is not JSON: %v", err)
	}
	if payload["brand_keywords"] != float64(1) || payload["non_brand_keywords"] != float64(1) || payload["source"] != "revserp" {
		t.Fatalf("event payload = %v, want counts plus revserp source", payload)
	}
	if store.enqueued != 0 {
		t.Fatalf("enqueued = %d, want no prompt_generation for keyword edits", store.enqueued)
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := body["brand_keywords"]; !ok {
		t.Fatalf("response = %v, want brand/non-brand lists", body)
	}
	if !strings.Contains(result.Summary, "user-defined preserved") {
		t.Fatalf("summary = %q, want user preservation note", result.Summary)
	}
}

func TestUpdateProjectKeywordsValidationErrorsStayModelVisible(t *testing.T) {
	store := newFakeUpdateStore("owner", nil)
	svc := newFakeProjectKeywordService()
	svc.replaceErr = fmt.Errorf("%w: brand list must not be empty", projectkeywords.ErrProjectKeywordInvalid)
	_, err := runApplyProjectKeywords(t, store, svc, &fakeSQLExec{}, `{"brand_keywords":["a"],"non_brand_keywords":["b"]}`)
	if err == nil || !isModelError(err) {
		t.Fatalf("service validation should be a model error, got %v", err)
	}
	svc.replaceErr = errors.New("connection refused")
	if _, err := runApplyProjectKeywords(t, store, svc, &fakeSQLExec{}, `{"brand_keywords":["a"],"non_brand_keywords":["b"]}`); err == nil || isModelError(err) {
		t.Fatalf("infrastructure failure should not be a model error, got %v", err)
	}
}

func TestProjectKeywordToolsRegistryAndCatalog(t *testing.T) {
	reg := NewRegistry()
	for _, name := range []string{"get_project_keywords", "update_project_keywords"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("registry missing %s", name)
		}
	}
	byName := map[string]Def{}
	for _, def := range CatalogDefs() {
		byName[def.Name] = def
	}
	get, ok := byName["get_project_keywords"]
	if !ok {
		t.Fatal("catalog missing get_project_keywords")
	}
	update, ok := byName["update_project_keywords"]
	if !ok {
		t.Fatal("catalog missing update_project_keywords")
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(update.Schema, &schema); err != nil {
		t.Fatalf("update schema: %v", err)
	}
	required, _ := schema["required"].([]interface{})
	has := func(list []interface{}, want string) bool {
		for _, item := range list {
			if item == want {
				return true
			}
		}
		return false
	}
	if !has(required, "brand_keywords") || !has(required, "non_brand_keywords") {
		t.Fatalf("update schema required = %v, want both lists", required)
	}
	props, _ := schema["properties"].(map[string]interface{})
	if len(props) != 2 {
		t.Fatalf("update schema properties = %v, want exactly the two lists", props)
	}
	for _, tenant := range []string{"project_id", "user_id", "organization_id", "source"} {
		if _, ok := props[tenant]; ok {
			t.Fatalf("update schema must not accept %q", tenant)
		}
	}
	if !strings.Contains(update.Description, "organization owner") {
		t.Fatalf("update description should state the owner gate, got %q", update.Description)
	}
	if !strings.Contains(get.Description, "update_project_keywords") {
		t.Fatalf("get description should point at the update tool, got %q", get.Description)
	}
}

func TestUpdateProjectKeywordsRejectsOversizedInputBeforeWrites(t *testing.T) {
	many := make([]string, 11)
	for i := range many {
		many[i] = "keyword-" + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10)) + "z"
	}
	raw, _ := json.Marshal(map[string]any{"brand_keywords": many, "non_brand_keywords": []string{"widgets"}})
	store := newFakeUpdateStore("owner", nil)
	svc := newFakeProjectKeywordService()
	if _, err := runApplyProjectKeywords(t, store, svc, &fakeSQLExec{}, string(raw)); err == nil || !isModelError(err) {
		t.Fatalf("11 unique phrases should be a model-visible limit error, got %v", err)
	} else if svc.replaceCalls != 0 {
		t.Fatalf("rejected oversized input must not write, calls = %d", svc.replaceCalls)
	}

	long := strings.Repeat("a", 201)
	if _, err := runApplyProjectKeywords(t, store, svc, &fakeSQLExec{}, `{"brand_keywords":["`+long+`"],"non_brand_keywords":["widgets"]}`); err == nil || !isModelError(err) {
		t.Fatalf("overlong phrase should be a model-visible error, got %v", err)
	} else if svc.replaceCalls != 0 {
		t.Fatalf("rejected overlong input must not write, calls = %d", svc.replaceCalls)
	}

	if _, err := runApplyProjectKeywords(t, store, svc, &fakeSQLExec{}, `{"brand_keywords":["a\u0000b"],"non_brand_keywords":["widgets"]}`); err == nil || !isModelError(err) {
		t.Fatalf("NUL phrase should be a model-visible error, got %v", err)
	} else if svc.replaceCalls != 0 {
		t.Fatalf("rejected NUL input must not write, calls = %d", svc.replaceCalls)
	}
}

func TestUpdateProjectKeywordsSchemaReflectsRuntimeLimits(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(json.RawMessage(updateProjectKeywordsSchema), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, name := range []string{"brand_keywords", "non_brand_keywords"} {
		prop, _ := props[name].(map[string]any)
		if prop == nil {
			t.Fatalf("schema missing %q", name)
		}
		if prop["maxItems"] != float64(10) {
			t.Fatalf("%s maxItems = %v, want 10", name, prop["maxItems"])
		}
		items, _ := prop["items"].(map[string]any)
		if items == nil || items["maxLength"] != float64(200) {
			t.Fatalf("%s items.maxLength = %v, want 200", name, items)
		}
	}
}
