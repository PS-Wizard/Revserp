// Package aichattools defines the catalog of tools the AI chat agent loop can
// invoke against a crawl. The catalog is inert by itself: nothing here writes
// to the chat, the worker, or any API path.
package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aiskills"
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
	UserID     pgtype.UUID
	ProjectID  pgtype.UUID
	CrawlID    pgtype.UUID
	LocationID pgtype.UUID
	Queries    *sqlc.Queries
	DB         Transactor
	// GSC is the search console data fetcher, nil when the worker has none.
	GSC GSCFetcher
	// RowBudget caps how many database rows one turn may fetch through tools.
	// Nil means no per-turn cap (raw tool-call mode); the agent loop sets it later.
	RowBudget *Budget
	// PageContentBudget caps total serialized page-content bytes and unique
	// content-page keys per turn. Nil means direct/raw mode without cumulative cap.
	PageContentBudget *PageContentBudget
	// Web is the web search and fetch path (TinyFish), nil when the worker has no
	// web key configured. The web tools report that as an unavailable state.
	Web WebClient
	// WebBudget caps web searches and fetches per turn. Nil means no cap.
	WebBudget *WebBudget
	// Suggest is the Google autocomplete reader, nil when the worker has none.
	// The suggestions tool reports that as an unavailable state.
	Suggest SuggestClient
	// SuggestBudget caps autocomplete calls and upstream requests per turn. Nil
	// means no cap. An expand costs about 27 requests, so it stops one turn from
	// spending many rounds of requests.
	SuggestBudget *SuggestBudget
	// SuppressPromptGeneration stops update_business_profile from enqueuing its
	// own prompt_generation follow-up. Setup chaining sets it because the setup
	// transaction owns that enqueue once it advances setup status. Chat leaves it
	// false, so a saved profile still triggers question generation.
	SuppressPromptGeneration bool
	// Skills is the read-only filesystem catalog of Git-managed guidance
	// skills, nil when unwired. The skill tools report nil as unavailable.
	Skills *aiskills.Catalog
	// SkillsBudget caps total skill file bytes read per turn. Nil means no cap.
	SkillsBudget *SkillBudget
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

// PageContentState is the durable form of one PageContentBudget: the byte
// remainder plus the unique-page limit and the keys already registered.
type PageContentState struct {
	BytesLeft   int
	UniqueLimit int
	SeenKeys    []string
}

// State reports the budget as a resumable snapshot. A nil budget reports a
// spent state so a restored turn is never handed fresh page allowance.
func (b *PageContentBudget) State() PageContentState {
	if b == nil {
		return PageContentState{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]string, 0, len(b.pages))
	for key := range b.pages {
		keys = append(keys, key)
	}
	return PageContentState{BytesLeft: b.remaining, UniqueLimit: b.uniqueLimit, SeenKeys: keys}
}

// RestorePageContentBudget rebuilds a budget from a snapshot. Keys already
// registered stay registered, so a resumed turn cannot claim fresh unique
// pages for content it already paid for; keys beyond the restored limit are
// dropped and the limit stays authoritative.
func RestorePageContentBudget(state PageContentState) *PageContentBudget {
	budget := NewPageContentBudget(state.BytesLeft, state.UniqueLimit)
	for _, key := range state.SeenKeys {
		budget.TryRegisterPage(key)
	}
	return budget
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
	return &Registry{tools: []Tool{readIssuesTool(), getScoreSummaryTool(), getSearchConsoleDataTool(), getBusinessProfileTool(), readIssueWorkTool(), readPageTool(), renderChartTool(), updateBusinessProfileTool(), getProjectKeywordsTool(), updateProjectKeywordsTool(), webSearchTool(), getSearchSuggestionsTool(), fetchURLTool(), getKeywordCoverageTool(), getLocationLandmarksTool(), listSkillsTool(), readSkillTool()}}
}

// CatalogDefs lists every native tool definition in catalog order, including
// native tools not yet served to the model. Admin gating and denylist
// validation run against the full catalog, so a tool can be gateable (and
// shown in the admin AI tools drawer) before the model can call it. MCP
// connection tools are not here: they exist per connection and are gated by
// the canonical aliases MCPModelToolName builds.
func CatalogDefs() []Def {
	return []Def{readIssuesTool().Def, getScoreSummaryTool().Def, getSearchConsoleDataTool().Def, getBusinessProfileTool().Def, readIssueWorkTool().Def, readPageTool().Def, renderChartTool().Def, updateBusinessProfileTool().Def, getProjectKeywordsTool().Def, updateProjectKeywordsTool().Def, webSearchTool().Def, getSearchSuggestionsTool().Def, fetchURLTool().Def, getKeywordCoverageTool().Def, getLocationLandmarksTool().Def, listSkillsTool().Def, readSkillTool().Def}
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

// Add registers one per-turn tool, rejecting empty or duplicate names so a
// dynamic MCP tool can never shadow or duplicate a native tool.
func (r *Registry) Add(tool Tool) error {
	if strings.TrimSpace(tool.Def.Name) == "" {
		return errors.New("aichattools: tool name must not be empty")
	}
	if _, ok := r.Get(tool.Def.Name); ok {
		return fmt.Errorf("aichattools: tool %q is already registered", tool.Def.Name)
	}
	r.tools = append(r.tools, tool)
	return nil
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

// NewFilteredRegistry returns a registry of the native served tools minus the
// blocked (denylist snapshot) names. MCP connection tools are added per turn
// with Add; the executing registry and the model-facing defs must derive from
// the same per-turn registry so the model cannot run a disabled or unexposed
// tool by guessing its name.
func NewFilteredRegistry(blocked []string) *Registry {
	deny := make(map[string]bool, len(blocked))
	for _, name := range blocked {
		deny[name] = true
	}
	registry := &Registry{}
	for _, tool := range NewRegistry().tools {
		if deny[tool.Def.Name] {
			continue
		}
		registry.tools = append(registry.tools, tool)
	}
	return registry
}

// LocationUnsupportedNativeTools lists native tools that read or write parent
// project data and have no location branch. A location-scoped turn denies them,
// so the model can never report parent totals or edit the parent while the user
// is inside a location.
func LocationUnsupportedNativeTools() []string {
	return []string{
		"get_score_summary",
		"read_issues",
		"read_issue_work",
		"get_search_console_data",
	}
}

const locationUpdateProjectKeywordsSchema = `{
  "type": "object",
  "required": ["brand_keywords", "non_brand_keywords", "source"],
  "properties": {
    "brand_keywords": {"type": "array", "items": {"type": "string", "maxLength": 200}, "description": "Complete keyword list for the chosen source. Replaces that source atomically; an empty array clears the selected list. Suggested (revserp) lists must be non-empty. Any count is allowed for selected."},
    "non_brand_keywords": {"type": "array", "items": {"type": "string", "maxLength": 200}, "description": "Complete keyword list for the chosen source. Replaces that source atomically; an empty array clears the selected list. Suggested (revserp) lists must be non-empty. Any count is allowed for selected."},
    "source": {"type": "string", "enum": ["selected", "revserp"], "description": "Required location-only write target. Pass selected only after the user explicitly asks to select Maps queries: it replaces the location selected list and syncs future paid Maps draft queries. Pass revserp from the Find-keywords flow to save a small relevant set of suggested lists; revserp writes never touch user-defined, selected, or Maps state."}
  },
  "additionalProperties": false
}`

const locationUpdateProjectKeywordsDescription = "Replace one of the current location's keyword lists. Both brand_keywords and non_brand_keywords are required as complete lists, plus the required location-only source naming the target: pass selected only after the user explicitly asks to select Maps queries (it replaces the selected list and syncs future paid Maps draft queries, and either list may be empty to clear it); pass revserp from the Find-keywords flow to save a small relevant set of suggested lists (must be non-empty, never touches user-defined, selected, or Maps state). Generated or suggested keywords never become selected Maps queries without an explicit selected write. Writes the location lists only, never the parent project keywords. Requires organization owner; non-owners are denied. Ground suggestions in the location profile, saved landmarks, and crawled content; never invent search volume or rank numbers. Keyword edits never trigger question generation."

func NewLocationScopedRegistry(blocked []string) *Registry {
	combined := make([]string, 0, len(blocked)+len(LocationUnsupportedNativeTools()))
	combined = append(combined, blocked...)
	combined = append(combined, LocationUnsupportedNativeTools()...)
	registry := NewFilteredRegistry(combined)
	registry.applyLocationDefs()
	return registry
}

func (r *Registry) applyLocationDefs() {
	for i := range r.tools {
		if r.tools[i].Def.Name != updateProjectKeywordsName {
			continue
		}
		r.tools[i].Def.Label = "Update location selected keywords"
		r.tools[i].Def.Description = locationUpdateProjectKeywordsDescription
		r.tools[i].Def.Schema = json.RawMessage(locationUpdateProjectKeywordsSchema)
	}
}
