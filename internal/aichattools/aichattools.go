// Package aichattools defines the catalog of tools the AI chat agent loop can
// invoke against a crawl. The catalog is inert by itself: nothing here writes
// to the chat, the worker, or any API path.
package aichattools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jackc/pgx/v5"
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

// Transactor can begin a transaction; implemented by *pgxpool.Pool and pgx.Tx.
type Transactor interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Scope carries server-derived identity and data access for one tool call.
// Tool schemas never contain tenant IDs; the loop fills these fields instead.
type Scope struct {
	UserID    pgtype.UUID
	ProjectID pgtype.UUID
	CrawlID   pgtype.UUID
	Queries   *sqlc.Queries
	DB        Transactor
	// GSC is the search console data fetcher, nil when the worker has none.
	GSC GSCFetcher
	// RowBudget caps how many database rows one turn may fetch through tools.
	// Nil means no per-turn cap (raw tool-call mode); the agent loop sets it later.
	RowBudget *Budget
	// PageContentBudget caps total serialized page-content bytes and unique
	// content-page keys per turn. Nil means direct/raw mode without cumulative cap.
	PageContentBudget *PageContentBudget
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

// PageContentBudget is a thread-safe per-turn cap for serialized page content
// bytes and unique attempted content-page keys. Calls execute sequentially,
// so the implementation is intentionally simple.
type PageContentBudget struct {
	mu          sync.Mutex
	remaining   int
	uniqueLimit int
	pages       map[string]struct{}
}

// NewPageContentBudget returns a budget with byteLimit bytes and
// uniquePageLimit unique page slots. Negative limits become zero.
func NewPageContentBudget(byteLimit, uniquePageLimit int) *PageContentBudget {
	if byteLimit < 0 {
		byteLimit = 0
	}
	if uniquePageLimit < 0 {
		uniquePageLimit = 0
	}
	return &PageContentBudget{
		remaining:   byteLimit,
		uniqueLimit: uniquePageLimit,
		pages:       make(map[string]struct{}),
	}
}

// RemainingBytes reports how many serialized content bytes remain.
func (b *PageContentBudget) RemainingBytes() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remaining
}

// SpendBytes consumes up to n bytes and returns the remaining count.
func (b *PageContentBudget) SpendBytes(n int) int {
	if b == nil {
		return 0
	}
	if n < 0 {
		n = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n >= b.remaining {
		b.remaining = 0
	} else {
		b.remaining -= n
	}
	return b.remaining
}

// TryRegisterPage attempts to reserve a unique content-page slot for key.
// The same key may be registered repeatedly without consuming another slot.
// A new key fails (returns false) after uniqueLimit distinct keys have
// been registered. Unavailable content still registers the page; metadata
// mode will not call the budget at all.
func (b *PageContentBudget) TryRegisterPage(key string) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pages[key]; ok {
		return true
	}
	if len(b.pages) >= b.uniqueLimit {
		return false
	}
	b.pages[key] = struct{}{}
	return true
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
	return &Registry{tools: []Tool{readIssuesTool(), getScoreSummaryTool(), getSearchConsoleDataTool(), getBusinessProfileTool(), readIssueWorkTool(), readPageTool(), renderChartTool(), updateBusinessProfileTool()}}
}

// CatalogDefs lists every implemented tool definition in catalog order,
// including tools not yet served to the model. Admin gating and denylist
// validation run against the full catalog, so a tool can be gateable (and
// shown in the admin AI tools drawer) before the model can call it.
func CatalogDefs() []Def {
	return []Def{readIssuesTool().Def, getScoreSummaryTool().Def, getSearchConsoleDataTool().Def, getBusinessProfileTool().Def, readIssueWorkTool().Def, readPageTool().Def, renderChartTool().Def, updateBusinessProfileTool().Def}
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
