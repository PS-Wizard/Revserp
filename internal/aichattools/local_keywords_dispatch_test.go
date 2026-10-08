package aichattools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
)

func TestExecuteDispatchesOnScopeLocationID(t *testing.T) {
	valid := Scope{LocationID: testLocationID}
	if _, err := executeGetProjectKeywords(context.Background(), json.RawMessage(`{}`), valid); err == nil ||
		!strings.Contains(err.Error(), "queries or transaction support") {
		t.Fatalf("get err = %v, want the local branch", err)
	}
	if _, err := executeGetProjectKeywords(context.Background(), json.RawMessage(`{}`), Scope{}); err == nil ||
		!strings.Contains(err.Error(), "scope has no queries") ||
		strings.Contains(err.Error(), "transaction support") {
		t.Fatalf("get err = %v, want the parent branch", err)
	}
	if _, err := executeUpdateProjectKeywords(context.Background(), json.RawMessage(`{}`), valid); err == nil ||
		!strings.Contains(err.Error(), "queries or transaction support") {
		t.Fatalf("update err = %v, want the local branch", err)
	}
	tool := getKeywordCoverageTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`), valid); err == nil ||
		!strings.Contains(err.Error(), "queries or transaction support") {
		t.Fatalf("coverage err = %v, want the local branch", err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`), Scope{}); err == nil ||
		!strings.Contains(err.Error(), "scope has no queries") {
		t.Fatalf("coverage err = %v, want the parent branch", err)
	}
}

func TestRunUpdateLocationKeywordsAcceptsBeyondParentCap(t *testing.T) {
	brand := make([]string, 0, 15)
	need := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		brand = append(brand, fmt.Sprintf("Brand phrase %d", i))
		need = append(need, fmt.Sprintf("Need phrase %d", i))
	}
	raw, _ := json.Marshal(map[string]any{"brand_keywords": brand, "non_brand_keywords": need, "source": "selected"})
	args, err := parseLocalKeywordArgs(raw)
	if err != nil {
		t.Fatalf("local args reject 15+15: %v", err)
	}
	gotBrand, gotNeed, err := locationkeywords.NormalizeKeywordGroup(args.BrandKeywords, args.NonBrandKeywords)
	if err != nil {
		t.Fatalf("local normalize rejects 15+15: %v", err)
	}
	if len(gotBrand) != 15 || len(gotNeed) != 15 {
		t.Fatalf("kept %d/%d, want 15/15: parent max-10 must not cap location lists", len(gotBrand), len(gotNeed))
	}
}

// newLocationDispatchScratch seeds the approved scratch database with one
// org, project, owner member, bound location, profile snapshot, and one
// selected keyword. It never touches any other database: the URL comes only
// from LOCATION_KEYWORDS_TEST_DATABASE_URL.
func newLocationDispatchScratch(t *testing.T) (context.Context, *pgxpool.Pool, Scope) {
	t.Helper()
	url := os.Getenv("LOCATION_KEYWORDS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LOCATION_KEYWORDS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, url, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("location dispatch scratch database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var orgID, projectID, locationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('dispatch-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'dispatch','https://dispatch.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, auth_provider, auth_subject, email) VALUES ($1,'dispatch-test','dispatch-user','dispatch@example.com') ON CONFLICT (id) DO NOTHING`, testUserID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, testUserID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,'Dispatchville',27.7,85.3) RETURNING id`, projectID).Scan(&locationID); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_business_profiles (project_id, location_id, brand_name, website_url, services) VALUES ($1,$2,'Acme','https://acme.example','["Plumber"]'::jsonb)`, projectID, locationID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'Old Pick','old pick','non_brand','selected')`, locationID); err != nil {
		t.Fatalf("seed keyword: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM organization_members WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return ctx, pool, Scope{DB: pool, Queries: sqlc.New(pool), ProjectID: projectID, UserID: testUserID, LocationID: locationID}
}

func TestExecuteGetProjectKeywordsLocalDispatch(t *testing.T) {
	ctx, _, scope := newLocationDispatchScratch(t)
	res, err := executeGetProjectKeywords(ctx, json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var lists map[string]locationkeywords.KeywordGroup
	if err := json.Unmarshal([]byte(res.Content), &lists); err != nil {
		t.Fatalf("content is not keyword JSON: %v\n%s", err, res.Content)
	}
	if len(lists["selected"].NonBranded) != 1 || lists["selected"].NonBranded[0] != "Old Pick" {
		t.Fatalf("selected = %s", res.Content)
	}
	found := false
	for _, s := range lists["revserp_suggested"].NonBranded {
		if s == "Plumber" {
			found = true
		}
	}
	if !found {
		t.Fatalf("suggested lacks snapshot service: %s", res.Content)
	}
}

func TestExecuteUpdateProjectKeywordsLocalDispatch(t *testing.T) {
	ctx, pool, scope := newLocationDispatchScratch(t)
	if _, err := pool.Exec(ctx, `INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin) VALUES ($1,'Stale Query','stale query',0,true,'map','manual','service')`, scope.LocationID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	brand := make([]string, 0, 12)
	need := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		brand = append(brand, fmt.Sprintf("Brand phrase %d", i))
		need = append(need, fmt.Sprintf("Need phrase %d", i))
	}
	raw, _ := json.Marshal(map[string]any{"brand_keywords": brand, "non_brand_keywords": need, "source": "selected"})
	res, err := executeUpdateProjectKeywords(ctx, raw, scope)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var body map[string][]string
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not keyword JSON: %v\n%s", err, res.Content)
	}
	if len(body["brand_keywords"]) != 12 || len(body["non_brand_keywords"]) != 12 {
		t.Fatalf("response = %s", res.Content)
	}
	var selected, user int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE source='selected'), COUNT(*) FILTER (WHERE source='user') FROM location_keywords WHERE location_id=$1`, scope.LocationID).Scan(&selected, &user); err != nil {
		t.Fatal(err)
	}
	if selected != 24 || user != 0 {
		t.Fatalf("stored selected=%d user=%d, want 24/0", selected, user)
	}
	var enabled int
	var stale bool
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE enabled), COALESCE(bool_or(normalized='stale query' AND enabled), false) FROM project_location_queries WHERE location_id=$1 AND kind='map'`, scope.LocationID).Scan(&enabled, &stale); err != nil {
		t.Fatal(err)
	}
	if enabled != 24 || stale {
		t.Fatalf("draft enabled=%d staleEnabled=%v, want 24/false", enabled, stale)
	}
}

func TestExecuteGetLocationKeywordCoverageDispatch(t *testing.T) {
	ctx, pool, scope := newLocationDispatchScratch(t)
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, source) VALUES ($1,'completed','manual') RETURNING id`, scope.ProjectID).Scan(&crawlID); err != nil {
		t.Fatalf("seed crawl: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, h1, status_code, content_type) VALUES ($1,'https://dispatch.example/old','Old Pick Repairs','Old Pick',200,'text/html')`, crawlID); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	tool := getKeywordCoverageTool()
	res, err := tool.Execute(ctx, json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var body keywordCoverageResponse
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not coverage JSON: %v\n%s", err, res.Content)
	}
	if body.TotalSeeds != 1 || body.Seeds[0].Keyword != "Old Pick" {
		t.Fatalf("coverage must key on the location selected list: %s", res.Content)
	}
}
