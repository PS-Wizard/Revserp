package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type mcpOrgRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type mcpListOrganizationsOutput struct {
	Organizations []mcpOrgRow `json:"organizations"`
}

type mcpListOrganizationsInput struct{}

type mcpGetProjectInput struct {
	ProjectID string `json:"project_id"`
}

type mcpCreateProjectInput struct {
	OrganizationID string `json:"organization_id,omitempty"`
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
}

type mcpCreateProjectOutput struct {
	Project mcpProjectRow `json:"project"`
	Message string        `json:"message,omitempty"`
}

type mcpListCrawlsInput struct {
	ProjectID string `json:"project_id"`
	Status    string `json:"status,omitempty"`
	Limit     int32  `json:"limit,omitempty"`
	Offset    int32  `json:"offset,omitempty"`
}

type mcpCrawlRow struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	Status          string `json:"status"`
	Phase           string `json:"phase,omitempty"`
	URLsDiscovered  int32  `json:"urls_discovered"`
	URLsCrawled     int32  `json:"urls_crawled"`
	MaxDepthReached int32  `json:"max_depth_reached"`
	SEOScore        *int32 `json:"seo_score,omitempty"`
	AEOScore        *int32 `json:"aeo_score,omitempty"`
	PageSpeedScore  *int32 `json:"pagespeed_score,omitempty"`
	OverallScore    *int32 `json:"overall_score,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	CompletedAt     string `json:"completed_at,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type mcpListCrawlsOutput struct {
	Crawls  []mcpCrawlRow `json:"crawls"`
	Limit   int32         `json:"limit"`
	Offset  int32         `json:"offset"`
	Count   int           `json:"count"`
	HasMore bool          `json:"has_more"`
}

type mcpGetCrawlInput struct {
	CrawlID string `json:"crawl_id"`
}

type mcpGetCrawlOutput struct {
	Crawl   mcpCrawlRow `json:"crawl"`
	Message string      `json:"message,omitempty"`
}

type mcpStartCrawlInput struct {
	ProjectID string `json:"project_id"`
	MaxPages  int32  `json:"max_pages,omitempty"`
	MaxDepth  int32  `json:"max_depth,omitempty"`
}

type mcpGetIssueInput struct {
	IssueID string `json:"issue_id"`
}

type mcpGetIssueOutput struct {
	Issue crawlIssueResponse `json:"issue"`
}

type mcpGetPageHealthInput struct {
	CrawlID   string `json:"crawl_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

type mcpGetPageHealthOutput struct {
	CrawlID    string  `json:"crawl_id,omitempty"`
	Buckets    []int64 `json:"buckets,omitempty"`
	TotalPages int64   `json:"total_pages,omitempty"`
	Message    string  `json:"message,omitempty"`
}

func (a *App) mcpListOrganizations(ctx context.Context, _ *mcp.CallToolRequest, _ mcpListOrganizationsInput) (*mcp.CallToolResult, mcpListOrganizationsOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpListOrganizationsOutput{}, err
	}
	out := mcpListOrganizationsOutput{Organizations: []mcpOrgRow{}}
	for _, org := range p.Organizations {
		enabled, err := a.mcpOrgIntegrationsEnabled(ctx, org.ID)
		if err != nil {
			return nil, mcpListOrganizationsOutput{}, err
		}
		if !enabled {
			continue
		}
		out.Organizations = append(out.Organizations, mcpOrgRow{
			ID:   org.ID.String(),
			Name: org.Name,
			Role: org.Role,
		})
	}
	return nil, out, nil
}

func (a *App) mcpGetProject(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetProjectInput) (*mcp.CallToolResult, mcpProjectRow, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpProjectRow{}, err
	}
	project, err := a.mcpProjectForUser(ctx, p, in.ProjectID)
	if err != nil {
		return nil, mcpProjectRow{}, err
	}
	return nil, mcpProjectRow{
		ID:             project.ID.String(),
		OrganizationID: project.OrganizationID.String(),
		Name:           project.Name,
		BaseURL:        project.BaseUrl,
	}, nil
}

func (a *App) mcpCreateProject(ctx context.Context, _ *mcp.CallToolRequest, in mcpCreateProjectInput) (*mcp.CallToolResult, mcpCreateProjectOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpCreateProjectOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	baseURL := strings.TrimSpace(in.BaseURL)
	if name == "" || baseURL == "" {
		return nil, mcpCreateProjectOutput{}, errors.New("name and base_url are required")
	}
	orgID, err := a.mcpResolveCreateOrg(ctx, p, in.OrganizationID)
	if err != nil {
		return nil, mcpCreateProjectOutput{}, err
	}
	normalized, err := crawler.NormalizeURL(baseURL, nil)
	if err != nil {
		return nil, mcpCreateProjectOutput{}, errors.New("invalid base_url")
	}
	if err := crawler.ValidatePublicHost(ctx, normalized.Hostname()); err != nil {
		return nil, mcpCreateProjectOutput{}, errors.New("base_url must not point to a private or internal host")
	}

	project, err := a.Queries.CreateProject(ctx, sqlc.CreateProjectParams{
		OrganizationID: orgID,
		Name:           name,
		BaseUrl:        baseURL,
	})
	if err != nil {
		return nil, mcpCreateProjectOutput{}, err
	}
	return nil, mcpCreateProjectOutput{
		Project: mcpProjectRow{
			ID:             project.ID.String(),
			OrganizationID: project.OrganizationID.String(),
			Name:           project.Name,
			BaseURL:        project.BaseUrl,
		},
		Message: "Project created. Call start_crawl with this project_id, then poll get_crawl until the crawl completes.",
	}, nil
}

func (a *App) mcpResolveCreateOrg(ctx context.Context, p Principal, organizationID string) (pgtype.UUID, error) {
	orgIDs, err := a.mcpAccessibleOrgIDs(ctx, p, organizationID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if strings.TrimSpace(organizationID) == "" {
		if len(orgIDs) == 1 {
			return orgIDs[0], nil
		}
		if len(orgIDs) == 0 {
			return pgtype.UUID{}, errors.New("no workspace available")
		}
		return pgtype.UUID{}, errors.New("provide organization_id; call list_organizations if you do not have it")
	}
	return orgIDs[0], nil
}

func (a *App) mcpListCrawls(ctx context.Context, _ *mcp.CallToolRequest, in mcpListCrawlsInput) (*mcp.CallToolResult, mcpListCrawlsOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpListCrawlsOutput{}, err
	}
	project, err := a.mcpProjectForUser(ctx, p, in.ProjectID)
	if err != nil {
		return nil, mcpListCrawlsOutput{}, err
	}
	status, err := mcpParseCrawlStatus(in.Status)
	if err != nil {
		return nil, mcpListCrawlsOutput{}, err
	}
	limit, offset, err := mcpNormalizePagination(in.Limit, in.Offset)
	if err != nil {
		return nil, mcpListCrawlsOutput{}, err
	}

	rows, err := a.Queries.ListCrawlsForProject(ctx, sqlc.ListCrawlsForProjectParams{
		ProjectID: project.ID,
		Column2:   status,
		Limit:     limit + 1,
		Offset:    offset,
	})
	if err != nil {
		return nil, mcpListCrawlsOutput{}, err
	}
	hasMore := len(rows) > int(limit)
	if hasMore {
		rows = rows[:limit]
	}
	out := mcpListCrawlsOutput{
		Crawls:  make([]mcpCrawlRow, 0, len(rows)),
		Limit:   limit,
		Offset:  offset,
		Count:   len(rows),
		HasMore: hasMore,
	}
	for _, row := range rows {
		out.Crawls = append(out.Crawls, mcpCrawlRowFromList(row))
	}
	return nil, out, nil
}

func (a *App) mcpGetCrawl(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetCrawlInput) (*mcp.CallToolResult, mcpGetCrawlOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}
	crawl, err := a.mcpCrawlForUser(ctx, p, in.CrawlID)
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}
	out := mcpGetCrawlOutput{Crawl: mcpCrawlRowFromGet(crawl)}
	out.Message = mcpCrawlPollMessage(crawl.Status)
	return nil, out, nil
}

func (a *App) mcpStartCrawl(ctx context.Context, _ *mcp.CallToolRequest, in mcpStartCrawlInput) (*mcp.CallToolResult, mcpGetCrawlOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}
	project, err := a.mcpProjectForUser(ctx, p, in.ProjectID)
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}

	active, err := a.Queries.HasActiveCrawlForProject(ctx, sqlc.HasActiveCrawlForProjectParams{
		ProjectID: project.ID,
		UserID:    p.User.ID,
	})
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}
	if active {
		existing, err := a.mcpLatestActiveCrawl(ctx, project.ID)
		if err != nil {
			return nil, mcpGetCrawlOutput{}, err
		}
		return nil, mcpGetCrawlOutput{
			Crawl:   existing,
			Message: "A crawl is already queued or running for this project. Poll get_crawl with this crawl_id until it finishes. Do not start another crawl.",
		}, nil
	}

	snapshot, err := mcpStartCrawlSnapshot(in.MaxPages, in.MaxDepth)
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}
	crawl, err := a.Queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID:         project.ID,
		RequestedByUserID: p.User.ID,
		Source:            "manual",
		Status:            "queued",
		ConfigSnapshot:    snapshot,
		StartedAt:         pgtype.Timestamptz{},
		CompetitorID:      pgtype.UUID{},
		ParentCrawlID:     pgtype.UUID{},
	})
	if err != nil {
		return nil, mcpGetCrawlOutput{}, err
	}
	return nil, mcpGetCrawlOutput{
		Crawl:   mcpCrawlRowFromCreate(crawl),
		Message: "Crawl queued. Poll get_crawl with this crawl_id until status is completed or failed. Do not invent scores or issues while it is running.",
	}, nil
}

func (a *App) mcpLatestActiveCrawl(ctx context.Context, projectID pgtype.UUID) (mcpCrawlRow, error) {
	for _, status := range []string{"running", "queued"} {
		rows, err := a.Queries.ListCrawlsForProject(ctx, sqlc.ListCrawlsForProjectParams{
			ProjectID: projectID,
			Column2:   status,
			Limit:     1,
			Offset:    0,
		})
		if err != nil {
			return mcpCrawlRow{}, err
		}
		if len(rows) > 0 {
			return mcpCrawlRowFromList(rows[0]), nil
		}
	}
	return mcpCrawlRow{}, errors.New("active crawl not found")
}

func mcpStartCrawlSnapshot(maxPages, maxDepth int32) ([]byte, error) {
	body := map[string]int32{}
	if maxDepth > 0 {
		body["max_depth"] = maxDepth
	}
	if maxPages > 0 {
		body["max_pages"] = maxPages
	}
	raw := []byte(`{}`)
	if len(body) > 0 {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	return normalizeCreateCrawlConfigSnapshot(raw)
}

func (a *App) mcpGetIssue(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetIssueInput) (*mcp.CallToolResult, mcpGetIssueOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpGetIssueOutput{}, err
	}
	if strings.TrimSpace(in.IssueID) == "" {
		return nil, mcpGetIssueOutput{}, errors.New("issue_id is required")
	}
	issueID, err := parseUUIDParam(in.IssueID)
	if err != nil {
		return nil, mcpGetIssueOutput{}, errors.New("invalid issue_id")
	}
	issue, err := a.Queries.GetCrawlIssueByIDForUser(ctx, sqlc.GetCrawlIssueByIDForUserParams{ID: issueID, UserID: p.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, mcpGetIssueOutput{}, errors.New("issue not found")
		}
		return nil, mcpGetIssueOutput{}, err
	}
	return nil, mcpGetIssueOutput{Issue: newFetchedCrawlIssueResponse(issue)}, nil
}

func (a *App) mcpGetPageHealth(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetPageHealthInput) (*mcp.CallToolResult, mcpGetPageHealthOutput, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, mcpGetPageHealthOutput{}, err
	}
	out := mcpGetPageHealthOutput{}
	crawlID, err := a.mcpResolveCrawl(ctx, p, in.CrawlID, in.ProjectID)
	if err != nil {
		return nil, mcpGetPageHealthOutput{}, err
	}
	if crawlID == nil {
		out.Message = mcpNoCompletedCrawlMessage("get_page_health")
		return nil, out, nil
	}

	rows, err := a.Queries.GetCrawlPageIssueHistogramForUser(ctx, sqlc.GetCrawlPageIssueHistogramForUserParams{
		CrawlID: *crawlID,
		UserID:  p.User.ID,
	})
	if err != nil {
		return nil, mcpGetPageHealthOutput{}, err
	}
	buckets := make([]int64, pageHealthBuckets)
	var total int64
	for _, row := range rows {
		index := int(row.IssueCount)
		if index < 0 || index >= pageHealthBuckets {
			continue
		}
		buckets[index] += row.PageCount
		total += row.PageCount
	}
	out.CrawlID = crawlID.String()
	out.Buckets = buckets
	out.TotalPages = total
	return nil, out, nil
}

func (a *App) mcpCrawlForUser(ctx context.Context, p Principal, crawlID string) (sqlc.GetCrawlByIDForUserRow, error) {
	if strings.TrimSpace(crawlID) == "" {
		return sqlc.GetCrawlByIDForUserRow{}, errors.New("crawl_id is required")
	}
	id, err := parseUUIDParam(crawlID)
	if err != nil {
		return sqlc.GetCrawlByIDForUserRow{}, errors.New("invalid crawl_id")
	}
	crawl, err := a.Queries.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: id, UserID: p.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GetCrawlByIDForUserRow{}, errors.New("crawl not found")
		}
		return sqlc.GetCrawlByIDForUserRow{}, err
	}
	if _, err := a.mcpProjectForUser(ctx, p, crawl.ProjectID.String()); err != nil {
		return sqlc.GetCrawlByIDForUserRow{}, errors.New("crawl not found")
	}
	return crawl, nil
}

func mcpParseCrawlStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return "", nil
	}
	switch CrawlStatus(status) {
	case CrawlStatusQueued, CrawlStatusRunning, CrawlStatusCompleted, CrawlStatusFailed, CrawlStatusCancelled:
		return status, nil
	default:
		return "", errors.New("invalid status")
	}
}

func mcpCrawlPollMessage(status string) string {
	switch CrawlStatus(status) {
	case CrawlStatusQueued, CrawlStatusRunning:
		return "Crawl is still in progress. Call get_crawl again with this crawl_id until status is completed or failed. Do not invent scores or issues yet."
	case CrawlStatusFailed:
		return "Crawl failed. Scores and issues are not available from this run."
	case CrawlStatusCancelled:
		return "Crawl was cancelled."
	default:
		return ""
	}
}

func mcpCrawlRowFromGet(crawl sqlc.GetCrawlByIDForUserRow) mcpCrawlRow {
	return mcpCrawlRowFromParts(
		crawl.ID, crawl.ProjectID, crawl.Status, crawl.Phase,
		crawl.UrlsDiscovered, crawl.UrlsCrawled, crawl.MaxDepthReached,
		crawl.SeoScore, crawl.AeoScore, crawl.PagespeedScore, crawl.OverallScore,
		crawl.StartedAt, crawl.CompletedAt, crawl.CreatedAt,
	)
}

func mcpCrawlRowFromList(crawl sqlc.ListCrawlsForProjectRow) mcpCrawlRow {
	return mcpCrawlRowFromParts(
		crawl.ID, crawl.ProjectID, crawl.Status, crawl.Phase,
		crawl.UrlsDiscovered, crawl.UrlsCrawled, crawl.MaxDepthReached,
		crawl.SeoScore, crawl.AeoScore, crawl.PagespeedScore, crawl.OverallScore,
		crawl.StartedAt, crawl.CompletedAt, crawl.CreatedAt,
	)
}

func mcpCrawlRowFromCreate(crawl sqlc.CreateCrawlRow) mcpCrawlRow {
	return mcpCrawlRowFromParts(
		crawl.ID, crawl.ProjectID, crawl.Status, crawl.Phase,
		crawl.UrlsDiscovered, crawl.UrlsCrawled, crawl.MaxDepthReached,
		crawl.SeoScore, crawl.AeoScore, crawl.PagespeedScore, crawl.OverallScore,
		crawl.StartedAt, crawl.CompletedAt, crawl.CreatedAt,
	)
}

func mcpCrawlRowFromParts(
	id, projectID pgtype.UUID,
	status string,
	phase pgtype.Text,
	urlsDiscovered, urlsCrawled, maxDepthReached int32,
	seo, aeo, pageSpeed, overall pgtype.Int4,
	startedAt, completedAt, createdAt pgtype.Timestamptz,
) mcpCrawlRow {
	row := mcpCrawlRow{
		ID:              id.String(),
		ProjectID:       projectID.String(),
		Status:          status,
		URLsDiscovered:  urlsDiscovered,
		URLsCrawled:     urlsCrawled,
		MaxDepthReached: maxDepthReached,
		SEOScore:        mcpInt32Ptr(seo),
		AEOScore:        mcpInt32Ptr(aeo),
		PageSpeedScore:  mcpInt32Ptr(pageSpeed),
		OverallScore:    mcpInt32Ptr(overall),
		CreatedAt:       formatTimestamp(createdAt),
	}
	if phase.Valid {
		row.Phase = phase.String
	}
	if startedAt.Valid {
		row.StartedAt = formatTimestamp(startedAt)
	}
	if completedAt.Valid {
		row.CompletedAt = formatTimestamp(completedAt)
	}
	return row
}
