// Package aitools implements the tool layer for the tool-calling
// Revserp AI agent. Every tool operates on the current org/project/crawl
// scope only; no tool schema accepts project_id, crawl_id, org_id, or
// user_id — tenant scope always comes from Scope, which the caller fills from
// the authenticated session, and every DB call goes through a "...ForUser" /
// org-scoped sqlc query.
package aitools

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Scope is the authenticated, tenant-bound context a tool executes under.
// It is assembled by the caller (the agent loop) from the session, never
// from model-supplied arguments.
type Scope struct {
	UserID    pgtype.UUID
	OrgID     pgtype.UUID
	ProjectID pgtype.UUID
	CrawlID   pgtype.UUID
	// Timezone is the caller's IANA timezone (browser-resolved), used as the
	// default when configure_auto_crawl omits one. May be empty.
	Timezone string
	Queries  *sqlc.Queries
}

// Result is one tool's output: Content goes back to the LLM, Summary is a
// one-line human-readable description for the UI. Optional metadata drives a
// persisted-before-emitted UI action for the corresponding tool.
type Result struct {
	Content        string
	Summary        string
	CrawlID        string
	CrawlProjectID string
	Destination    string
	ProjectID      string
	// CompareProjectID/CompareCrawlID together drive a "compare_started" action,
	// opening the cross-project comparison view against that project's crawl.
	CompareProjectID string
	CompareCrawlID   string
	ExportAction     *ExportAction
	// Chart, when set, drives a "chart" SSE event that renders a visualization
	// in the chat. It does not go back to the model (Content does).
	Chart *ChartSpec
}

// Tool pairs a model-facing definition with its scoped executor.
type Tool struct {
	Def     ai.ToolDef
	Execute func(ctx context.Context, args json.RawMessage, s Scope) (Result, error)
}

// Registry is an ordered collection of tools, keyed by name.
type Registry struct {
	order []string
	tools map[string]Tool
}

func newRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) register(t Tool) {
	if _, exists := r.tools[t.Def.Name]; exists {
		panic("aitools: duplicate tool name: " + t.Def.Name)
	}
	r.order = append(r.order, t.Def.Name)
	r.tools[t.Def.Name] = t
}

// Defs returns the tool definitions in registration order, for building a
// TurnRequest.Tools list.
func (r *Registry) Defs() []ai.ToolDef {
	defs := make([]ai.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].Def)
	}
	return defs
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Deps holds the application-owned, authorized write paths the mutating tools
// depend on. A nil field makes the corresponding tool report itself as
// unavailable at execution time.
type Deps struct {
	CreateCrawl           CrawlCreator
	ConfigureAutoCrawl    AutoCrawlConfigurer
	UpdateBusinessProfile BusinessProfileUpdater
	ReadSearchConsole     SearchConsoleReader
}

// NewRegistry builds the registry of all 17 tools available to the agent.
func NewRegistry(deps Deps) *Registry {
	r := newRegistry()
	r.register(listProjectsTool())
	r.register(switchProjectTool())
	r.register(businessProfileTool())
	r.register(updateBusinessProfileTool(deps.UpdateBusinessProfile))
	r.register(scoreSummaryTool())
	r.register(listIssuesTool())
	r.register(recommendedFixTool())
	r.register(pageContentTool())
	r.register(listPagesTool())
	r.register(searchConsoleTool(deps.ReadSearchConsole))
	r.register(startCrawlTool(deps.CreateCrawl))
	r.register(configureAutoCrawlTool(deps.ConfigureAutoCrawl))
	r.register(exportCrawlTool())
	r.register(exportAuditTool())
	r.register(navigateTool())
	r.register(compareProjectsTool())
	r.register(renderChartTool())
	return r
}
