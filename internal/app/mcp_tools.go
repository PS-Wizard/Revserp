package app

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// registerMCPTools attaches MCP tools. Keep this file as the only place
// AddTool is called. Tools are read-only and resolve the principal from
// request context (requirePrincipal already ran on the /mcp chain).
func (a *App) registerMCPTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_projects",
		Description: "List Revserp projects the signed-in user can access. " +
			"Pass organization_id (UUID) to narrow to one workspace; omit it to list every workspace the user belongs to. " +
			"Returns id, organization_id, name and base_url for each project. " +
			"If the list is empty, the account has no projects yet: tell the user to open the Revserp app, create a project for their site, and run the first crawl, then call this tool again. Do not invent data.",
	}, a.mcpListProjects)

	mcp.AddTool(server, &mcp.Tool{
		Name: "read_issues",
		Description: "List crawl issues (SEO/AEO findings) for one Revserp crawl. " +
			"Pass crawl_id (UUID) directly, or pass project_id (UUID) to use that project's latest completed crawl. One of the two is required. " +
			"Pagination: limit (default 50, max 200) and offset. The result reports limit, offset, count and total. " +
			"If the project has no completed crawl yet, the result carries a message instead of issues: tell the user to run a crawl in the Revserp app, then call this tool again. Do not invent issues.",
	}, a.mcpReadIssues)
}

type mcpProjectRow struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
}

type mcpListProjectsOutput struct {
	Projects []mcpProjectRow `json:"projects"`
	Message  string          `json:"message,omitempty"`
}

type mcpListProjectsInput struct {
	OrganizationID string `json:"organization_id,omitempty"`
}

type mcpCrawlSummary struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
}

type mcpIssueRow struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Pillar    string `json:"pillar"`
	Bucket    string `json:"bucket"`
	IssueType string `json:"issue_type"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

type mcpReadIssuesOutput struct {
	Crawl   *mcpCrawlSummary `json:"crawl,omitempty"`
	Issues  []mcpIssueRow    `json:"issues"`
	Limit   int32            `json:"limit"`
	Offset  int32            `json:"offset"`
	Count   int              `json:"count"`
	Total   int64            `json:"total"`
	Message string           `json:"message,omitempty"`
}

type mcpReadIssuesInput struct {
	CrawlID   string `json:"crawl_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Limit     int32  `json:"limit,omitempty"`
	Offset    int32  `json:"offset,omitempty"`
}

func (a *App) mcpListProjects(ctx context.Context, _ *mcp.CallToolRequest, in mcpListProjectsInput) (*mcp.CallToolResult, mcpListProjectsOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpListProjectsOutput{}, err
	}

	orgIDs, err := a.mcpAccessibleOrgIDs(ctx, p, in.OrganizationID)
	if err != nil {
		return nil, mcpListProjectsOutput{}, err
	}

	out := mcpListProjectsOutput{Projects: []mcpProjectRow{}}
	for _, orgID := range orgIDs {
		projects, err := a.Queries.ListProjectsForOrganization(ctx, orgID)
		if err != nil {
			return nil, mcpListProjectsOutput{}, err
		}
		for _, project := range projects {
			out.Projects = append(out.Projects, mcpProjectRow{
				ID:             project.ID.String(),
				OrganizationID: project.OrganizationID.String(),
				Name:           project.Name,
				BaseURL:        project.BaseUrl,
			})
		}
	}
	if len(out.Projects) == 0 {
		out.Message = "This Revserp account has no projects yet. Tell the user to open the Revserp app, create a project for their site, and run the first crawl, then call this tool again. Do not invent project data."
	}
	return nil, out, nil
}

func (a *App) mcpReadIssues(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadIssuesInput) (*mcp.CallToolResult, mcpReadIssuesOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpReadIssuesOutput{}, err
	}

	limit, offset, err := mcpNormalizePagination(in.Limit, in.Offset)
	if err != nil {
		return nil, mcpReadIssuesOutput{}, err
	}

	out := mcpReadIssuesOutput{Issues: []mcpIssueRow{}}
	crawlID, err := a.mcpResolveCrawl(ctx, p, in, &out)
	if err != nil {
		return nil, mcpReadIssuesOutput{}, err
	}
	if crawlID == nil {
		// Empty state already recorded on out.Message.
		return nil, out, nil
	}

	total, err := a.Queries.CountCrawlIssuesForCrawlByUser(ctx, sqlc.CountCrawlIssuesForCrawlByUserParams{CrawlID: *crawlID, UserID: p.User.ID})
	if err != nil {
		return nil, mcpReadIssuesOutput{}, err
	}
	rows, err := a.Queries.ListCrawlIssuesForCrawlByUser(ctx, sqlc.ListCrawlIssuesForCrawlByUserParams{
		CrawlID: *crawlID,
		UserID:  p.User.ID,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return nil, mcpReadIssuesOutput{}, err
	}
	for _, row := range rows {
		out.Issues = append(out.Issues, mcpIssueRow{
			ID:        row.ID.String(),
			URL:       row.Url,
			Pillar:    row.Pillar,
			Bucket:    row.Bucket,
			IssueType: row.IssueType,
			Severity:  row.Severity,
			Message:   row.Message,
		})
	}
	out.Limit = limit
	out.Offset = offset
	out.Count = len(rows)
	out.Total = total
	return nil, out, nil
}

// mcpAccessibleOrgIDs resolves the orgs the tool call may read. With an
// explicit organization_id it enforces membership and reports not-found (the
// caller gets the same message whether the org is missing or foreign).
func (a *App) mcpAccessibleOrgIDs(ctx context.Context, p Principal, organizationID string) ([]pgtype.UUID, error) {
	if strings.TrimSpace(organizationID) == "" {
		orgIDs := make([]pgtype.UUID, 0, len(p.Organizations))
		for _, org := range p.Organizations {
			orgIDs = append(orgIDs, org.ID)
		}
		return orgIDs, nil
	}
	orgID, err := parseUUIDParam(organizationID)
	if err != nil {
		return nil, errors.New("invalid organization_id")
	}
	if _, err := a.Queries.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{OrgID: orgID, UserID: p.User.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("organization not found")
		}
		return nil, err
	}
	return []pgtype.UUID{orgID}, nil
}

// mcpResolveCrawl finds the crawl read_issues should report. Returns a nil
// crawl ID only for the no-completed-crawl empty state, which is recorded in
// out.Message. Membership is enforced with the existing ForUser queries.
func (a *App) mcpResolveCrawl(ctx context.Context, p Principal, in mcpReadIssuesInput, out *mcpReadIssuesOutput) (*pgtype.UUID, error) {
	switch {
	case strings.TrimSpace(in.CrawlID) != "":
		id, err := parseUUIDParam(in.CrawlID)
		if err != nil {
			return nil, errors.New("invalid crawl_id")
		}
		crawl, err := a.Queries.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: id, UserID: p.User.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errors.New("crawl not found")
			}
			return nil, err
		}
		out.Crawl = mcpCrawlSummaryFromRow(crawl.ID, crawl.ProjectID, crawl.Status)
		return &crawl.ID, nil

	case strings.TrimSpace(in.ProjectID) != "":
		id, err := parseUUIDParam(in.ProjectID)
		if err != nil {
			return nil, errors.New("invalid project_id")
		}
		if _, err := a.Queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: id, UserID: p.User.ID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errors.New("project not found")
			}
			return nil, err
		}
		crawls, err := a.Queries.ListCrawlsForProject(ctx, sqlc.ListCrawlsForProjectParams{
			ProjectID: id,
			Column2:   "completed",
			Limit:     1,
			Offset:    0,
		})
		if err != nil {
			return nil, err
		}
		if len(crawls) == 0 {
			out.Message = "This project has no completed crawl yet. Tell the user to run a crawl in the Revserp app, then call read_issues again. Do not invent issues."
			return nil, nil
		}
		out.Crawl = mcpCrawlSummaryFromRow(crawls[0].ID, crawls[0].ProjectID, crawls[0].Status)
		return &crawls[0].ID, nil

	default:
		return nil, errors.New("provide either crawl_id or project_id")
	}
}

func mcpCrawlSummaryFromRow(id, projectID pgtype.UUID, status string) *mcpCrawlSummary {
	return &mcpCrawlSummary{
		ID:        id.String(),
		ProjectID: projectID.String(),
		Status:    status,
	}
}

// mcpNormalizePagination mirrors parsePaginationParams for tool arguments:
// non-positive limit falls back to the default, large limits are capped, and
// offsets must not be negative.
func mcpNormalizePagination(limit int32, offset int32) (int32, int32, error) {
	if limit <= 0 {
		limit = defaultPaginationLimit
	}
	if limit > maxPaginationLimit {
		limit = maxPaginationLimit
	}
	if offset < 0 {
		return 0, 0, errors.New("invalid offset")
	}
	return limit, offset, nil
}

// mcpPrincipal reads the principal attached by requirePrincipal. Missing
// principal is a tool error, not a panic.
func mcpPrincipal(ctx context.Context) (Principal, error) {
	p, ok := principalFromContext(ctx)
	if !ok || !p.User.ID.Valid {
		return Principal{}, errors.New("unauthorized: no principal in context")
	}
	return p, nil
}
