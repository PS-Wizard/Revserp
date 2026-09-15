package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// mcpAIChatRegistry is the in-app AI catalog. MCP reuses the executors so
// scoring, GSC, profile, page, and work stay on one code path. Schemas stay
// tenant-free; MCP adds project_id / crawl_id around the call.
var mcpAIChatRegistry = aichattools.NewRegistry()

type mcpAIChatOutput struct {
	Content string `json:"content"`
}

type mcpGetScoreSummaryInput struct {
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id,omitempty"`
	Pillar    string `json:"pillar,omitempty"`
	Limit     int32  `json:"limit,omitempty"`
}

type mcpGetBusinessProfileInput struct {
	ProjectID          string `json:"project_id"`
	IncludeSeedPrompts bool   `json:"include_seed_prompts,omitempty"`
}

type mcpGetSearchConsoleDataInput struct {
	ProjectID string   `json:"project_id"`
	Reports   []string `json:"reports"`
	Days      int32    `json:"days,omitempty"`
	StartDate string   `json:"start_date,omitempty"`
	EndDate   string   `json:"end_date,omitempty"`
	Search    string   `json:"search,omitempty"`
	Limit     int32    `json:"limit,omitempty"`
	Offset    int32    `json:"offset,omitempty"`
}

type mcpReadPageInput struct {
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id,omitempty"`
	URL       string `json:"url"`
	Mode      string `json:"mode"`
	Cursor    string `json:"cursor,omitempty"`
}

type mcpReadIssueWorkInput struct {
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Pillar    string `json:"pillar,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	IssueType string `json:"issue_type,omitempty"`
	Limit     int32  `json:"limit,omitempty"`
	Offset    int32  `json:"offset,omitempty"`
}

type mcpReadIssuesInput struct {
	ProjectID string   `json:"project_id,omitempty"`
	CrawlID   string   `json:"crawl_id,omitempty"`
	Pillars   []string `json:"pillars,omitempty"`
	Bucket    string   `json:"bucket,omitempty"`
	IssueType string   `json:"issue_type,omitempty"`
	Severity  string   `json:"severity,omitempty"`
	URLs      []string `json:"urls,omitempty"`
	Limit     int32    `json:"limit,omitempty"`
	Offset    int32    `json:"offset,omitempty"`
}

type mcpUpdateBusinessProfileInput struct {
	ProjectID           string   `json:"project_id"`
	BrandName           string   `json:"brand_name,omitempty"`
	WebsiteURL          string   `json:"website_url,omitempty"`
	PrimaryCategory     string   `json:"primary_category,omitempty"`
	PrimaryLocation     string   `json:"primary_location,omitempty"`
	BusinessDescription string   `json:"business_description,omitempty"`
	SeedPrompts         []string `json:"seed_prompts,omitempty"`
	TargetKeywords      []string `json:"target_keywords,omitempty"`
}

func (a *App) mcpGetScoreSummary(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetScoreSummaryInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "get_score_summary", in.ProjectID, in.CrawlID, true, in)
}

func (a *App) mcpGetBusinessProfile(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetBusinessProfileInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "get_business_profile", in.ProjectID, "", false, in)
}

func (a *App) mcpGetSearchConsoleData(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetSearchConsoleDataInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "get_search_console_data", in.ProjectID, "", false, in)
}

func (a *App) mcpReadPage(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadPageInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "read_page", in.ProjectID, in.CrawlID, true, in)
}

func (a *App) mcpReadIssueWork(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadIssueWorkInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "read_issue_work", in.ProjectID, in.CrawlID, true, in)
}

func (a *App) mcpReadIssues(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadIssuesInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "read_issues", in.ProjectID, in.CrawlID, true, in)
}

func (a *App) mcpUpdateBusinessProfile(ctx context.Context, _ *mcp.CallToolRequest, in mcpUpdateBusinessProfileInput) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	return a.mcpInvokeAIChat(ctx, "update_business_profile", in.ProjectID, "", false, in)
}

func (a *App) mcpInvokeAIChat(ctx context.Context, name, projectID, crawlID string, needCrawl bool, args any) (*mcp.CallToolResult, mcpAIChatOutput, error) {
	scope, guidance, err := a.mcpAIScope(ctx, projectID, crawlID, needCrawl)
	if err != nil {
		return nil, mcpAIChatOutput{}, err
	}
	if guidance != "" {
		return nil, mcpAIChatOutput{Content: guidance}, nil
	}
	tool, ok := mcpAIChatRegistry.Get(name)
	if !ok {
		return nil, mcpAIChatOutput{}, errors.New("unknown tool")
	}
	raw, err := mcpArgsWithoutScopeIDs(args)
	if err != nil {
		return nil, mcpAIChatOutput{}, err
	}
	result, err := tool.Execute(ctx, raw, scope)
	if err != nil {
		return nil, mcpAIChatOutput{}, err
	}
	return nil, mcpAIChatOutput{Content: result.Content}, nil
}

func (a *App) mcpAIScope(ctx context.Context, projectID, crawlID string, needCrawl bool) (aichattools.Scope, string, error) {
	p, err := mcpPrincipal(ctx)
	if err != nil {
		return aichattools.Scope{}, "", err
	}
	if strings.TrimSpace(projectID) == "" && strings.TrimSpace(crawlID) == "" {
		if needCrawl {
			return aichattools.Scope{}, "", errors.New("provide project_id or crawl_id")
		}
		return aichattools.Scope{}, "", errors.New("project_id is required")
	}

	var project sqlc.Project
	if strings.TrimSpace(projectID) != "" {
		project, err = a.mcpProjectForUser(ctx, p, projectID)
		if err != nil {
			return aichattools.Scope{}, "", err
		}
	}

	scope := aichattools.Scope{
		UserID:  p.User.ID,
		Queries: a.Queries,
	}
	if a.DB != nil {
		scope.DB = a.DB
	}
	if a.GSCService != nil {
		scope.GSC = a.GSCService
	}

	if strings.TrimSpace(crawlID) != "" {
		crawl, err := a.mcpCrawlForUser(ctx, p, crawlID)
		if err != nil {
			return aichattools.Scope{}, "", err
		}
		if project.ID.Valid && crawl.ProjectID != project.ID {
			return aichattools.Scope{}, "", errors.New("crawl does not belong to project")
		}
		if !project.ID.Valid {
			project, err = a.Queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{
				ID:     crawl.ProjectID,
				UserID: p.User.ID,
			})
			if err != nil {
				return aichattools.Scope{}, "", err
			}
		}
		scope.ProjectID = project.ID
		scope.CrawlID = crawl.ID
		return scope, "", nil
	}

	scope.ProjectID = project.ID
	if !needCrawl {
		return scope, "", nil
	}

	crawls, err := a.Queries.ListCrawlsForProject(ctx, sqlc.ListCrawlsForProjectParams{
		ProjectID: project.ID,
		Column2:   "completed",
		Limit:     1,
		Offset:    0,
	})
	if err != nil {
		return aichattools.Scope{}, "", err
	}
	if len(crawls) == 0 {
		return aichattools.Scope{}, mcpNoCompletedCrawlMessage("this tool"), nil
	}
	scope.CrawlID = crawls[0].ID
	return scope, "", nil
}

func mcpArgsWithoutScopeIDs(args any) (json.RawMessage, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return json.RawMessage(`{}`), nil
	}
	delete(obj, "project_id")
	delete(obj, "crawl_id")
	if len(obj) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(obj)
}
