// Package aichattools defines the catalog of tools the AI chat agent loop can
// invoke against a crawl. The catalog is inert by itself: nothing here writes
// to the chat, the worker, or any API path.
package aichattools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Def is the static, model-facing description of one tool.
type Def struct {
	Name        string
	Label       string
	Description string
	Schema      json.RawMessage
	// Feature names an organization feature flag the tool depends on (for
	// example gsc_connector). Empty means no feature dependency. Only the
	// admin catalog uses it — the model never sees it.
	Feature string
}

// Scope carries server-derived identity and data access for one tool call.
// Tool schemas never contain tenant IDs; the loop fills these fields instead.
type Scope struct {
	UserID    pgtype.UUID
	ProjectID pgtype.UUID
	CrawlID   pgtype.UUID
	Queries   *sqlc.Queries
	// GSC is the search console data fetcher, nil when the worker has none.
	GSC GSCFetcher
	// RowBudget caps how many database rows one turn may fetch through tools.
	// Nil means no per-turn cap (raw tool-call mode); the agent loop sets it later.
	RowBudget *Budget
}

// Budget is a thread-safe counter of rows a turn may still fetch.
type Budget struct {
	mu        sync.Mutex
	remaining int
}

// NewBudget returns a budget with rows available to spend.
func NewBudget(rows int) *Budget {
	return &Budget{remaining: rows}
}

// Remaining reports how many rows may still be fetched.
func (b *Budget) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remaining
}

// Spend consumes up to n rows and returns the remaining count, never below zero.
func (b *Budget) Spend(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n >= b.remaining {
		b.remaining = 0
	} else {
		b.remaining -= n
	}
	return b.remaining
}

// Result is one completed tool call: model-facing content plus a UI one-liner.
type Result struct {
	Content string
	Summary string
}

// Tool binds a tool's definition to its executor.
type Tool struct {
	Def     Def
	Execute func(ctx context.Context, args json.RawMessage, s Scope) (Result, error)
}

// Registry is an ordered catalog of tools.
type Registry struct {
	tools []Tool
}

// NewRegistry returns the registry of tools currently served to the model.
func NewRegistry() *Registry {
	return &Registry{tools: []Tool{readIssuesTool(), getScoreSummaryTool(), getSearchConsoleDataTool(), getBusinessProfileTool(), readIssueWorkTool(), renderChartTool()}}
}

// CatalogDefs lists every implemented tool definition in catalog order,
// including tools not yet served to the model. Admin gating and denylist
// validation run against the full catalog, so a tool can be gateable (and
// shown in the admin AI tools drawer) before the model can call it.
func CatalogDefs() []Def {
	return []Def{readIssuesTool().Def, getScoreSummaryTool().Def, getSearchConsoleDataTool().Def, getBusinessProfileTool().Def, readIssueWorkTool().Def, renderChartTool().Def}
}

// ToolFeatures maps every tool with a feature dependency to its feature flag
// name. Admin gating force-disables those tools when the flag is off.
func ToolFeatures() map[string]string {
	features := make(map[string]string)
	for _, def := range CatalogDefs() {
		if def.Feature != "" {
			features[def.Name] = def.Feature
		}
	}
	return features
}

// Names lists registered tool names in registration order.
func (r *Registry) Names() []string {
	names := make([]string, len(r.tools))
	for i, tool := range r.tools {
		names[i] = tool.Def.Name
	}
	return names
}

// Defs lists registered tool definitions in registration order.
func (r *Registry) Defs() []Def {
	defs := make([]Def, len(r.tools))
	for i, tool := range r.tools {
		defs[i] = tool.Def
	}
	return defs
}

// Get returns the named tool and whether it is registered.
func (r *Registry) Get(name string) (Tool, bool) {
	for _, tool := range r.tools {
		if tool.Def.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}
