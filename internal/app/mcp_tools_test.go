package app

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// TestRegisterMCPToolsDoesNotPanic guards AddTool input-schema inference: the
// SDK panics at registration time for types it cannot turn into a JSON schema.
func TestRegisterMCPToolsDoesNotPanic(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "revserp-test", Version: "0.0.0"}, nil)
	a := &App{}
	a.registerMCPTools(server)
}

// newMCPTestApp builds one App plus a principal context for a user who owns
// the test workspace. Skipped like the other DB-backed app tests when no
// DATABASE_URL is available.
func newMCPTestApp(t *testing.T) (context.Context, *App, *pgxpool.Pool, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('mcp-tools-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	p := Principal{
		User: sqlc.User{ID: userID},
		Organizations: []sqlc.ListOrganizationsForUserRow{
			{ID: orgID, Name: "mcp-tools-test-org", Role: "owner"},
		},
	}
	return withPrincipal(ctx, p), &App{Queries: queries}, pool, orgID
}

func newMCPTestProject(t *testing.T, pool *pgxpool.Pool, ctx context.Context, orgID pgtype.UUID, name string) pgtype.UUID {
	t.Helper()
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, $2, 'https://example.com') RETURNING id`, orgID, name).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	return projectID
}

func newMCPTestCrawl(t *testing.T, pool *pgxpool.Pool, ctx context.Context, projectID pgtype.UUID, status string, age string) pgtype.UUID {
	t.Helper()
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, created_at) VALUES ($1, $2, now() - $3::interval) RETURNING id`, projectID, status, age).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	return crawlID
}

func newMCPTestIssue(t *testing.T, pool *pgxpool.Pool, ctx context.Context, crawlID pgtype.UUID, url string, severity string) pgtype.UUID {
	t.Helper()
	var issueID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1, $2, 'seo', 'serp_metadata', 'missing_title', $3, 'msg', 'det') RETURNING id`, crawlID, url, severity).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	return issueID
}

func callMCPTool[In, Out any](t *testing.T, handler func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error), ctx context.Context, input In) (Out, error) {
	t.Helper()
	_, out, err := handler(ctx, &mcp.CallToolRequest{}, input)
	return out, err
}

func TestMCPListProjectsEmptyAccountGetsGuidance(t *testing.T) {
	ctx, a, _, _ := newMCPTestApp(t)

	out, err := callMCPTool(t, a.mcpListProjects, ctx, mcpListProjectsInput{})
	if err != nil {
		t.Fatalf("list_projects on empty account: %v", err)
	}
	if len(out.Projects) != 0 {
		t.Fatalf("expected no projects, got %d", len(out.Projects))
	}
	if !strings.Contains(out.Message, "Revserp app") || !strings.Contains(out.Message, "Do not invent") {
		t.Errorf("empty-state message missing guidance: %q", out.Message)
	}
}

func TestMCPListProjectsReturnsCompactRows(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	newMCPTestProject(t, pool, ctx, orgID, "mcp-compact-project")

	out, err := callMCPTool(t, a.mcpListProjects, ctx, mcpListProjectsInput{})
	if err != nil {
		t.Fatalf("list_projects: %v", err)
	}
	if len(out.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(out.Projects))
	}
	project := out.Projects[0]
	if project.Name != "mcp-compact-project" || project.BaseURL != "https://example.com" {
		t.Errorf("unexpected project row: %+v", project)
	}
	if project.ID == "" || project.OrganizationID != orgID.String() {
		t.Errorf("project ids missing: %+v", project)
	}
}

func TestMCPListProjectsForeignOrgIsNotFound(t *testing.T) {
	ctx, a, pool, _ := newMCPTestApp(t)
	foreignOrg := createFeaturesTestOrg(t, ctx, pool)

	_, err := callMCPTool(t, a.mcpListProjects, ctx, mcpListProjectsInput{OrganizationID: foreignOrg.String()})
	if err == nil || !strings.Contains(err.Error(), "organization not found") {
		t.Fatalf("want organization not found, got %v", err)
	}

	if _, err := callMCPTool(t, a.mcpListProjects, ctx, mcpListProjectsInput{OrganizationID: "not-a-uuid"}); err == nil || !strings.Contains(err.Error(), "invalid organization_id") {
		t.Errorf("want invalid organization_id, got %v", err)
	}
}

func TestMCPReadIssuesResolvesLatestCompletedCrawl(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-issues-project")

	older := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "2 hours")
	newMCPTestIssue(t, pool, ctx, older, "https://example.com/old", "high")
	newMCPTestIssue(t, pool, ctx, older, "https://example.com/old2", "low")
	latest := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "1 hour")
	latestIssue := newMCPTestIssue(t, pool, ctx, latest, "https://example.com/new", "medium")
	newMCPTestCrawl(t, pool, ctx, projectID, "running", "30 minutes")

	out, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("read_issues by project: %v", err)
	}
	if out.Crawl == nil || out.Crawl.ID != latest.String() || out.Crawl.Status != "completed" {
		t.Fatalf("expected latest completed crawl %s, got %+v", latest, out.Crawl)
	}
	if out.Total != 1 || out.Count != 1 {
		t.Fatalf("counts wrong: count=%d total=%d", out.Count, out.Total)
	}
	if out.Limit != defaultPaginationLimit || out.Offset != 0 {
		t.Errorf("pagination defaults wrong: limit=%d offset=%d", out.Limit, out.Offset)
	}
	if len(out.Issues) != 1 || out.Issues[0].ID != latestIssue.String() {
		t.Fatalf("expected the latest crawl's issue, got %+v", out.Issues)
	}
	issue := out.Issues[0]
	if issue.URL != "https://example.com/new" || issue.Severity != "medium" || issue.Pillar != "seo" || issue.Bucket != "serp_metadata" || issue.IssueType != "missing_title" || issue.Message != "msg" {
		t.Errorf("issue row fields wrong: %+v", issue)
	}

	// Direct crawl_id must bypass the latest-completed resolution.
	byID, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: older.String()})
	if err != nil {
		t.Fatalf("read_issues by crawl_id: %v", err)
	}
	if byID.Total != 2 || len(byID.Issues) != 2 {
		t.Errorf("expected 2 issues from the older crawl, got count=%d total=%d", len(byID.Issues), byID.Total)
	}

	// Cap the limit like /v1 does.
	capped, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: older.String(), Limit: 1000})
	if err != nil {
		t.Fatalf("read_issues with big limit: %v", err)
	}
	if capped.Limit != maxPaginationLimit {
		t.Errorf("limit cap wrong: %d", capped.Limit)
	}
}

func TestMCPReadIssuesPaginationPages(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-page-project")
	crawlID := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "1 hour")
	first := newMCPTestIssue(t, pool, ctx, crawlID, "https://example.com/page-a", "high")
	second := newMCPTestIssue(t, pool, ctx, crawlID, "https://example.com/page-b", "medium")
	third := newMCPTestIssue(t, pool, ctx, crawlID, "https://example.com/page-c", "low")
	for i, id := range []pgtype.UUID{first, second, third} {
		if _, err := pool.Exec(ctx, `UPDATE crawl_issues SET created_at = now() - ($2 * interval '1 minute') WHERE id = $1`, id, 30-i); err != nil {
			t.Fatal(err)
		}
	}

	page0, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: crawlID.String(), Limit: 1, Offset: 0})
	if err != nil {
		t.Fatalf("page 0: %v", err)
	}
	page1, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: crawlID.String(), Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	page2, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: crawlID.String(), Limit: 1, Offset: 2})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	past, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: crawlID.String(), Limit: 1, Offset: 3})
	if err != nil {
		t.Fatalf("past end: %v", err)
	}

	for _, page := range []mcpReadIssuesOutput{page0, page1, page2} {
		if page.Total != 3 || page.Limit != 1 || page.Count != 1 || len(page.Issues) != 1 {
			t.Fatalf("unexpected page meta: %+v", page)
		}
	}
	if page0.Offset != 0 || page1.Offset != 1 || page2.Offset != 2 {
		t.Fatalf("offsets: %d %d %d", page0.Offset, page1.Offset, page2.Offset)
	}
	got := []string{page0.Issues[0].ID, page1.Issues[0].ID, page2.Issues[0].ID}
	want := []string{first.String(), second.String(), third.String()}
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("page ids = %v, want %v", got, want)
	}
	if page0.Issues[0].ID == page1.Issues[0].ID || page1.Issues[0].ID == page2.Issues[0].ID {
		t.Fatal("pages returned overlapping issues")
	}
	if past.Total != 3 || past.Count != 0 || len(past.Issues) != 0 || past.Offset != 3 {
		t.Fatalf("past-end page wrong: %+v", past)
	}
}

func TestMCPReadIssuesNoCompletedCrawlGetsGuidance(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-no-crawl-project")
	newMCPTestCrawl(t, pool, ctx, projectID, "running", "10 minutes")

	out, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("read_issues with no completed crawl must not error: %v", err)
	}
	if out.Crawl != nil || out.Total != 0 || len(out.Issues) != 0 {
		t.Fatalf("expected empty result, got %+v", out)
	}
	if !strings.Contains(out.Message, "Revserp app") || !strings.Contains(out.Message, "Do not invent") {
		t.Errorf("empty-state message missing guidance: %q", out.Message)
	}
}

func TestMCPReadIssuesForeignCrawlNotFound(t *testing.T) {
	ctx, a, pool, _ := newMCPTestApp(t)
	foreignOrg := createFeaturesTestOrg(t, ctx, pool)
	foreignProject := newMCPTestProject(t, pool, ctx, foreignOrg, "mcp-foreign-project")
	foreignCrawl := newMCPTestCrawl(t, pool, ctx, foreignProject, "completed", "1 hour")

	_, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: foreignCrawl.String()})
	if err == nil || !strings.Contains(err.Error(), "crawl not found") {
		t.Fatalf("want crawl not found, got %v", err)
	}

	foreignProjectID := foreignProject
	_, err = callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{ProjectID: foreignProjectID.String()})
	if err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("want project not found, got %v", err)
	}
}

func TestMCPReadIssuesRequiresCrawlOrProject(t *testing.T) {
	ctx, a, _, _ := newMCPTestApp(t)

	_, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{})
	if err == nil || !strings.Contains(err.Error(), "provide either crawl_id or project_id") {
		t.Fatalf("want argument error, got %v", err)
	}
	if _, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: "not-a-uuid"}); err == nil || !strings.Contains(err.Error(), "invalid crawl_id") {
		t.Errorf("want invalid crawl_id, got %v", err)
	}
}

func TestMCPNormalizePagination(t *testing.T) {
	for _, tc := range []struct {
		limit  int32
		offset int32
		wantL  int32
		wantO  int32
		wantE  string
	}{
		{limit: 0, offset: 0, wantL: defaultPaginationLimit, wantO: 0},
		{limit: 1000, offset: 0, wantL: maxPaginationLimit, wantO: 0},
		{limit: 10, offset: 5, wantL: 10, wantO: 5},
		{limit: 10, offset: -1, wantE: "invalid offset"},
	} {
		limit, offset, err := mcpNormalizePagination(tc.limit, tc.offset)
		if tc.wantE != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantE) {
				t.Errorf("limit=%d offset=%d: want error %q, got %v", tc.limit, tc.offset, tc.wantE, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("limit=%d offset=%d: unexpected error %v", tc.limit, tc.offset, err)
			continue
		}
		if limit != tc.wantL || offset != tc.wantO {
			t.Errorf("limit=%d offset=%d: got %d/%d, want %d/%d", tc.limit, tc.offset, limit, offset, tc.wantL, tc.wantO)
		}
	}
}

func TestMCPToolsNeedPrincipal(t *testing.T) {
	queries, _, _ := newFeaturesTestQueries(t)
	a := &App{Queries: queries}

	if _, err := callMCPTool(t, a.mcpListProjects, context.Background(), mcpListProjectsInput{}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("list_projects without principal: want unauthorized error, got %v", err)
	}
	if _, err := callMCPTool(t, a.mcpReadIssues, context.Background(), mcpReadIssuesInput{ProjectID: "00000000-0000-0000-0000-000000000000"}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("read_issues without principal: want unauthorized error, got %v", err)
	}
}
