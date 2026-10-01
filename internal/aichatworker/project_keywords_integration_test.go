package aichatworker

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

func TestProjectKeywordToolPersistenceAndProfileRollback(t *testing.T) {
	databaseURL := os.Getenv("PROJECT_KEYWORDS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PROJECT_KEYWORDS_TEST_DATABASE_URL is not set")
	}
	t.Setenv("DATABASE_URL", databaseURL)
	worker, _, userID, projectID := testWorker(t)
	ctx := context.Background()
	queries := sqlc.New(worker.pool)
	if _, err := worker.pool.Exec(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source) VALUES ($1,'Acme','acme','brand','user')`, projectID); err != nil {
		t.Fatal(err)
	}
	scope := aichattools.Scope{UserID: userID, ProjectID: projectID, Queries: queries, DB: worker.pool}
	registry := aichattools.NewRegistry()
	updateKeywords, ok := registry.Get("update_project_keywords")
	if !ok {
		t.Fatal("update_project_keywords is not registered")
	}
	result, err := updateKeywords.Execute(ctx, json.RawMessage(`{"brand_keywords":["ACME"],"non_brand_keywords":["trail boots"]}`), scope)
	if err != nil || strings.Contains(result.Content, " error:") {
		t.Fatalf("keyword update failed: %v %s", err, result.Content)
	}
	before, err := projectkeywords.LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.UserDefined) != 1 || len(before.RevserpSuggested) != 2 || len(before.Combined) != 2 || before.Combined[0].Keyword != "Acme" || len(before.Combined[0].Sources) != 2 {
		t.Fatalf("persisted keyword lists lost user data or provenance: %#v", before)
	}
	var keywordEvents, promptJobs int
	if err := worker.pool.QueryRow(ctx, `SELECT count(*) FROM organization_events WHERE project_id=$1 AND event_type='project_keywords.updated'`, projectID).Scan(&keywordEvents); err != nil {
		t.Fatal(err)
	}
	if err := worker.pool.QueryRow(ctx, `SELECT count(*) FROM ai_worker_jobs WHERE project_id=$1 AND job_type='prompt_generation'`, projectID).Scan(&promptJobs); err != nil {
		t.Fatal(err)
	}
	if keywordEvents != 1 || promptJobs != 0 {
		t.Fatalf("keyword update emitted %d events and queued %d question jobs, want 1 and 0", keywordEvents, promptJobs)
	}

	updateProfile, ok := registry.Get("update_business_profile")
	if !ok {
		t.Fatal("update_business_profile is not registered")
	}
	args, err := json.Marshal(map[string]any{
		"brand_name": "invalid\x00brand", "website_url": "https://example.test",
		"branded_keywords": []string{"replacement brand"}, "non_branded_keywords": []string{"replacement service"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateProfile.Execute(ctx, args, scope); err == nil {
		t.Fatal("profile write accepted text PostgreSQL cannot store")
	}
	after, err := projectkeywords.LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed profile creation changed keyword rows: before=%#v after=%#v", before, after)
	}
	var profiles int
	if err := worker.pool.QueryRow(ctx, `SELECT count(*) FROM project_business_profile WHERE project_id=$1`, projectID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 {
		t.Fatalf("failed profile creation left %d profiles", profiles)
	}
}
