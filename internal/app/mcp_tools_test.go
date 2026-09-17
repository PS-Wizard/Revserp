package app

import (
	"context"
	"encoding/json"
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
	return withPrincipal(ctx, p), &App{Queries: queries, DB: pool}, pool, orgID
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
	if !strings.Contains(out.Message, "create_project") || !strings.Contains(out.Message, "Do not invent") {
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

type mcpReadIssuesPayload struct {
	TotalMatching int64 `json:"total_matching"`
	Issues        []struct {
		URL            string `json:"url"`
		Severity       string `json:"severity"`
		RecommendedFix string `json:"recommended_fix"`
	} `json:"issues"`
	HasMore    bool `json:"has_more"`
	NextOffset int  `json:"next_offset"`
}

func parseMCPReadIssuesContent(t *testing.T, content string) mcpReadIssuesPayload {
	t.Helper()
	var payload mcpReadIssuesPayload
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("read_issues content is not the in-app JSON: %v\n%s", err, content)
	}
	return payload
}

func TestMCPReadIssuesWrapsInAppExecutor(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-issues-project")

	older := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "2 hours")
	newMCPTestIssue(t, pool, ctx, older, "https://example.com/old", "high")
	newMCPTestIssue(t, pool, ctx, older, "https://example.com/old2", "low")
	latest := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "1 hour")
	newMCPTestIssue(t, pool, ctx, latest, "https://example.com/new", "medium")
	newMCPTestCrawl(t, pool, ctx, projectID, "running", "30 minutes")

	out, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("read_issues by project: %v", err)
	}
	payload := parseMCPReadIssuesContent(t, out.Content)
	if payload.TotalMatching != 1 || len(payload.Issues) != 1 {
		t.Fatalf("expected latest crawl's one issue, got %+v from %s", payload, out.Content)
	}
	if payload.Issues[0].URL != "https://example.com/new" || payload.Issues[0].Severity != "medium" {
		t.Errorf("issue row fields wrong: %+v", payload.Issues[0])
	}
	if payload.Issues[0].RecommendedFix == "" {
		t.Error("expected in-app recommended_fix on the row")
	}

	byID, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: older.String()})
	if err != nil {
		t.Fatalf("read_issues by crawl_id: %v", err)
	}
	olderPayload := parseMCPReadIssuesContent(t, byID.Content)
	if olderPayload.TotalMatching != 2 || len(olderPayload.Issues) != 2 {
		t.Errorf("expected 2 issues from the older crawl, got %+v", olderPayload)
	}

	capped, err := callMCPTool(t, a.mcpReadIssues, ctx, mcpReadIssuesInput{CrawlID: older.String(), Limit: 1000})
	if err != nil {
		t.Fatalf("read_issues with big limit: %v", err)
	}
	cappedPayload := parseMCPReadIssuesContent(t, capped.Content)
	if cappedPayload.TotalMatching != 2 || len(cappedPayload.Issues) != 2 {
		t.Errorf("in-app cap should still return both rows: %+v", cappedPayload)
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

	p0 := parseMCPReadIssuesContent(t, page0.Content)
	p1 := parseMCPReadIssuesContent(t, page1.Content)
	p2 := parseMCPReadIssuesContent(t, page2.Content)
	pPast := parseMCPReadIssuesContent(t, past.Content)
	if p0.TotalMatching != 3 || p1.TotalMatching != 3 || p2.TotalMatching != 3 {
		t.Fatalf("total_matching: %d %d %d", p0.TotalMatching, p1.TotalMatching, p2.TotalMatching)
	}
	if len(p0.Issues) != 1 || len(p1.Issues) != 1 || len(p2.Issues) != 1 {
		t.Fatalf("page sizes: %d %d %d", len(p0.Issues), len(p1.Issues), len(p2.Issues))
	}
	if p0.Issues[0].URL == p1.Issues[0].URL || p1.Issues[0].URL == p2.Issues[0].URL {
		t.Fatal("pages returned overlapping issues")
	}
	if pPast.TotalMatching != 3 || len(pPast.Issues) != 0 || pPast.HasMore {
		t.Fatalf("past-end page wrong: %+v", pPast)
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
	if !strings.Contains(out.Content, "start_crawl") || !strings.Contains(out.Content, "Do not invent") {
		t.Errorf("empty-state message missing guidance: %q", out.Content)
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
	if err == nil || !strings.Contains(err.Error(), "provide project_id or crawl_id") {
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
	if _, err := callMCPTool(t, a.mcpGetProject, context.Background(), mcpGetProjectInput{ProjectID: "00000000-0000-0000-0000-000000000000"}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("get_project without principal: want unauthorized error, got %v", err)
	}
}

func TestMCPListOrganizationsAndGetProject(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-get-project")

	orgs, err := callMCPTool(t, a.mcpListOrganizations, ctx, mcpListOrganizationsInput{})
	if err != nil {
		t.Fatalf("list_organizations: %v", err)
	}
	if len(orgs.Organizations) != 1 || orgs.Organizations[0].ID != orgID.String() {
		t.Fatalf("unexpected orgs: %+v", orgs)
	}

	project, err := callMCPTool(t, a.mcpGetProject, ctx, mcpGetProjectInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("get_project: %v", err)
	}
	if project.Name != "mcp-get-project" || project.OrganizationID != orgID.String() {
		t.Fatalf("unexpected project: %+v", project)
	}

	foreign := createFeaturesTestOrg(t, ctx, pool)
	var foreignProject pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'foreign', 'https://example.com') RETURNING id`, foreign).Scan(&foreignProject); err != nil {
		t.Fatal(err)
	}
	if _, err := callMCPTool(t, a.mcpGetProject, ctx, mcpGetProjectInput{ProjectID: foreignProject.String()}); err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("foreign project: want not found, got %v", err)
	}
}

func TestMCPGetIssueAndCrawls(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-issue-project")
	crawlID := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "1 hour")
	issueID := newMCPTestIssue(t, pool, ctx, crawlID, "https://example.com/", "high")

	issue, err := callMCPTool(t, a.mcpGetIssue, ctx, mcpGetIssueInput{IssueID: issueID.String()})
	if err != nil {
		t.Fatalf("get_issue: %v", err)
	}
	if issue.Issue.ID != issueID.String() || issue.Issue.URL != "https://example.com/" || issue.Issue.Details != "det" {
		t.Fatalf("unexpected issue: %+v", issue.Issue)
	}

	listed, err := callMCPTool(t, a.mcpListCrawls, ctx, mcpListCrawlsInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("list_crawls: %v", err)
	}
	if listed.Count != 1 || listed.Crawls[0].ID != crawlID.String() || listed.Crawls[0].Status != "completed" {
		t.Fatalf("unexpected crawls: %+v", listed)
	}

	got, err := callMCPTool(t, a.mcpGetCrawl, ctx, mcpGetCrawlInput{CrawlID: crawlID.String()})
	if err != nil {
		t.Fatalf("get_crawl: %v", err)
	}
	if got.Crawl.ID != crawlID.String() || got.Message != "" {
		t.Fatalf("unexpected crawl: %+v", got)
	}

	health, err := callMCPTool(t, a.mcpGetPageHealth, ctx, mcpGetPageHealthInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("get_page_health: %v", err)
	}
	if health.CrawlID != crawlID.String() || len(health.Buckets) != pageHealthBuckets {
		t.Fatalf("unexpected page health: %+v", health)
	}
}

func TestMCPCreateProjectAndStartCrawl(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)

	created, err := callMCPTool(t, a.mcpCreateProject, ctx, mcpCreateProjectInput{
		Name:    "mcp-created",
		BaseURL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("create_project: %v", err)
	}
	if created.Project.Name != "mcp-created" || created.Project.OrganizationID != orgID.String() || created.Project.ID == "" {
		t.Fatalf("unexpected created project: %+v", created)
	}
	if !strings.Contains(created.Message, "start_crawl") {
		t.Errorf("create message missing start_crawl: %q", created.Message)
	}

	started, err := callMCPTool(t, a.mcpStartCrawl, ctx, mcpStartCrawlInput{ProjectID: created.Project.ID})
	if err != nil {
		t.Fatalf("start_crawl: %v", err)
	}
	if started.Crawl.Status != "queued" || started.Crawl.ProjectID != created.Project.ID {
		t.Fatalf("unexpected started crawl: %+v", started)
	}
	if !strings.Contains(started.Message, "Poll get_crawl") {
		t.Errorf("start message missing poll guidance: %q", started.Message)
	}

	again, err := callMCPTool(t, a.mcpStartCrawl, ctx, mcpStartCrawlInput{ProjectID: created.Project.ID})
	if err != nil {
		t.Fatalf("start_crawl while active: %v", err)
	}
	if again.Crawl.ID != started.Crawl.ID || !strings.Contains(again.Message, "already") {
		t.Fatalf("expected existing active crawl, got %+v", again)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM crawls WHERE project_id = $1`, created.Project.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued %d crawls, want 1", n)
	}
}

// create_project over MCP must respect the same workspace cap as the HTTP route.
// Without this the limit is trivially bypassable by any MCP client.
func TestMCPCreateProjectRefusesAtWorkspaceLimit(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)

	if err := a.Queries.UpsertOrganizationFeatures(ctx, capParams(orgID, 1)); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}

	if _, err := callMCPTool(t, a.mcpCreateProject, ctx, mcpCreateProjectInput{
		Name:    "mcp-cap-first",
		BaseURL: "https://93.184.216.34",
	}); err != nil {
		t.Fatalf("first create_project: %v", err)
	}

	_, err := callMCPTool(t, a.mcpCreateProject, ctx, mcpCreateProjectInput{
		Name:    "mcp-cap-second",
		BaseURL: "https://93.184.216.34",
	})
	if err == nil || !strings.Contains(err.Error(), "project_limit_reached") {
		t.Fatalf("second create_project error = %v, want project_limit_reached", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE organization_id = $1`, orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("organization has %d projects, want the refused create to leave 1", n)
	}
}

func TestMCPWrappedAIChatTools(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "mcp-aichat-project")
	crawlID := newMCPTestCrawl(t, pool, ctx, projectID, "completed", "1 hour")

	summary, err := callMCPTool(t, a.mcpGetScoreSummary, ctx, mcpGetScoreSummaryInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("get_score_summary: %v", err)
	}
	if !strings.Contains(summary.Content, crawlID.String()) && !strings.Contains(summary.Content, "overall_score") {
		t.Fatalf("score summary missing crawl/score JSON: %q", summary.Content)
	}

	profile, err := callMCPTool(t, a.mcpGetBusinessProfile, ctx, mcpGetBusinessProfileInput{ProjectID: projectID.String()})
	if err != nil {
		t.Fatalf("get_business_profile: %v", err)
	}
	if !strings.Contains(profile.Content, "No business profile") {
		t.Fatalf("expected missing-profile message, got %q", profile.Content)
	}

	gsc, err := callMCPTool(t, a.mcpGetSearchConsoleData, ctx, mcpGetSearchConsoleDataInput{
		ProjectID: projectID.String(),
		Reports:   []string{"summary"},
	})
	if err != nil {
		t.Fatalf("get_search_console_data: %v", err)
	}
	if !strings.Contains(strings.ToLower(gsc.Content), "search console") {
		t.Fatalf("expected GSC unavailable copy, got %q", gsc.Content)
	}

	page, err := callMCPTool(t, a.mcpReadPage, ctx, mcpReadPageInput{
		ProjectID: projectID.String(),
		URL:       "https://example.com/",
		Mode:      "metadata",
	})
	if err != nil {
		t.Fatalf("read_page: %v", err)
	}
	if page.Content == "" {
		t.Fatal("read_page returned empty content")
	}

	updated, err := callMCPTool(t, a.mcpUpdateBusinessProfile, ctx, mcpUpdateBusinessProfileInput{
		ProjectID:  projectID.String(),
		BrandName:  "Revketer",
		WebsiteURL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("update_business_profile: %v", err)
	}
	if strings.Contains(updated.Content, "error:") {
		t.Fatalf("profile update failed: %q", updated.Content)
	}
}

func TestMCPIntegrationsOffHidesWorkspace(t *testing.T) {
	ctx, a, pool, orgID := newMCPTestApp(t)
	projectID := newMCPTestProject(t, pool, ctx, orgID, "gated")

	listed, err := callMCPTool(t, a.mcpListProjects, ctx, mcpListProjectsInput{})
	if err != nil {
		t.Fatalf("list_projects before gate: %v", err)
	}
	if len(listed.Projects) != 1 {
		t.Fatalf("list_projects before gate = %+v, want 1 project", listed)
	}

	if err := a.Queries.UpsertOrganizationFeatures(ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: true, GscConnector: true, AiChat: true, Integrations: false,
		AiMonthlyMessageLimit: 50, AiConcurrentTurnLimitPerUser: 2,
		AiVisibilityAuditMonthlyLimit: 10, MaxCompetitors: 3,
		AiAllowedReasoningEfforts: canonicalAIReasoningEfforts,
	}); err != nil {
		t.Fatalf("disable integrations: %v", err)
	}

	listed, err = callMCPTool(t, a.mcpListProjects, ctx, mcpListProjectsInput{})
	if err != nil {
		t.Fatalf("list_projects after gate: %v", err)
	}
	if len(listed.Projects) != 0 {
		t.Fatalf("list_projects after gate = %+v, want empty", listed)
	}

	orgs, err := callMCPTool(t, a.mcpListOrganizations, ctx, mcpListOrganizationsInput{})
	if err != nil {
		t.Fatalf("list_organizations after gate: %v", err)
	}
	if len(orgs.Organizations) != 0 {
		t.Fatalf("list_organizations after gate = %+v, want empty", orgs)
	}

	if _, err := callMCPTool(t, a.mcpGetProject, ctx, mcpGetProjectInput{ProjectID: projectID.String()}); err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("get_project after gate: %v", err)
	}
}
