package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	// businessProfileBootstrapEffort is fixed: bootstrapping is one-shot,
	// unattended, and benefits from deeper inference.
	businessProfileBootstrapEffort = "high"

	businessProfileBootstrapUpdateTool = "update_business_profile"
	businessProfileBootstrapReadTool   = "read_page"
	businessProfileBootstrapSearchTool = "web_search"
	businessProfileBootstrapFetchTool  = "fetch_url"

	// businessProfileBootstrapMaxRounds matches the chat worker's agent
	// round cap (aichatworker maxAgentRounds). It is a whole-job budget that
	// both the initial and the corrective attempt draw from.
	businessProfileBootstrapMaxRounds = 8

	// businessProfileBootstrapMaxPageContentReads matches the chat worker's
	// per-turn content-page allowance (aichatworker pageContentBudgetPages).
	// It is a whole-job budget enforced directly so a breach fails the job
	// instead of silently degrading to a "limit reached" tool result.
	businessProfileBootstrapMaxPageContentReads = 5

	// businessProfileBootstrapToolResultCap matches the chat worker's stored
	// tool-result cap (aichatworker toolResultContentCap).
	businessProfileBootstrapToolResultCap = 32 << 10

	businessProfileBootstrapPageListLimit = 20
	businessProfileBootstrapRowBudget     = 200
	businessProfileBootstrapPageBytes     = 96 << 10
	businessProfileBootstrapPagePages     = businessProfileBootstrapMaxPageContentReads
	businessProfileBootstrapWebSearches   = 3
	businessProfileBootstrapWebFetches    = 3

	businessProfileBootstrapDefaultModel = "deepseek-flash"

	businessProfileBootstrapDuplicatePageMessage = "read_page already read this page in this job; do not read the same page twice"
)

// businessProfileBootstrapReadTools are the read-only research and crawl
// context tools the normal AI chat already offers. They reuse the existing
// implementations and their authorization and output limits.
//
// get_search_console_data is intentionally absent: it depends on the Google
// Search Console integration this worker does not wire and it refreshes Google
// connection tokens, which is a write side effect on shared state.
// render_chart is absent because it is a presentation tool, not research.
var businessProfileBootstrapReadTools = []string{
	"read_issues",
	"get_score_summary",
	"get_business_profile",
	"read_issue_work",
	businessProfileBootstrapReadTool,
	businessProfileBootstrapSearchTool,
	businessProfileBootstrapFetchTool,
}

// businessProfileBootstrapWriteTools is the single write tool the bootstrap may
// call. No other mutating tool is ever exposed.
var businessProfileBootstrapWriteTools = []string{
	businessProfileBootstrapUpdateTool,
}

// businessProfileBootstrapTools is the strict allowlist exposed to the model.
var businessProfileBootstrapTools = append(append([]string{}, businessProfileBootstrapReadTools...), businessProfileBootstrapWriteTools...)

// businessProfileBootstrapScalarFields and businessProfileBootstrapArrayFields
// mirror the update_business_profile schema. The bootstrap is stricter than the
// chat PATCH contract: this job must create one complete profile, so every
// field is required and non-empty.
var businessProfileBootstrapScalarFields = []string{
	"brand_name",
	"website_url",
	"primary_category",
	"primary_location",
	"business_description",
	"product_description",
	"target_audience",
}

var businessProfileBootstrapArrayFields = []string{
	"business_competitors",
	"branded_keywords",
	"non_branded_keywords",
	"seed_prompts",
	"target_keywords",
}

const businessProfileBootstrapSystemPrompt = `You are an automated business-profile bootstrap agent. The organization owner already asked for this project's setup and gave standing authorization to create the business profile. You are not having a conversation and nobody will answer you.

Fixed rules:
- Never ask a question, request clarification, or wait for confirmation.
- Research before you write. Start with get_score_summary for the site's overall state, then read_issues, read_issue_work, or get_business_profile for context.
- Then inspect only representative high-value crawl pages with read_page: home, about, services, products, pricing, contact. Use web_search or fetch_url only when the crawl is still thin for a field.
- Do not enumerate or read every crawled page. Never read the same page twice, and stop researching as soon as you have enough evidence for every field.
- You may make at most 5 read_page content calls. Exceeding that cap fails the job.
- Before you finish you MUST make exactly one update_business_profile call, and that single call must succeed. The job fails if you never call it, call it more than once, or its result is an error. Plain text, a question, or a summary is never a valid result.
- The call is validated before it runs. It must include every field below, all strings non-blank, all arrays non-empty with non-blank values, and seed_prompts at most 5. Any missing or placeholder value makes the call fail.

Populate every field in that one call. Never leave a string or array empty:
- brand_name: the business's real brand name. Use the project name when the site does not show a clearer one.
- website_url: the project's base URL, keeping its exact host.
- primary_category: the single clearest category the business competes in.
- primary_location: the primary geographic market as a concrete place when the evidence points to one. Use "Online" or "Global" only when the business is clearly location-independent.
- business_description: a factual 1-2 sentence description of what the business does.
- product_description: what the business sells or offers.
- target_audience: the main customer segment.
- business_competitors: 3-8 real, named competitors.
- branded_keywords: the brand name plus obvious brand variants.
- non_branded_keywords: 5-10 category and need keywords with no brand terms.
- target_keywords: 5-10 search phrases the business should rank for.
- seed_prompts: up to 5 short, natural seed questions a potential customer might ask.

Inference is expected. When crawl or web evidence is thin, infer sensible values from the project name, domain, category, products, and likely market. A plausible, specific value is required and is better than an empty one. Never use generic placeholder tokens such as "Unknown", "N/A", "TBD", or "Not specified". Every list must be non-empty.

Call update_business_profile exactly one time now, with every field populated.`

const businessProfileBootstrapCorrectionPrompt = `Your previous attempt did not produce a successful update_business_profile call. Do not ask anything. Do not read more pages unless a field is still unknown, and do not exceed the remaining read_page content budget. Call update_business_profile exactly one time now and populate every field: non-empty strings, non-empty arrays, and up to 5 seed_prompts.`

// bootstrapToolExecutor runs one tool call. Production uses
// executeBusinessProfileBootstrapTool; tests substitute a fake.
type bootstrapToolExecutor func(ctx context.Context, registry *aichattools.Registry, call ai.ToolCall, scope aichattools.Scope) (string, aichattools.Result)

// bootstrapRunBudget is the whole-job research budget shared by the initial
// attempt and the corrective retry. Provider rounds and unique content-page
// reads are counted here so a retry cannot double the allowance, and repeated
// content reads are rejected without executing the tool.
type bootstrapRunBudget struct {
	roundsLeft           int
	pageContentReadsLeft int
	contentPageURLs      map[string]bool
}

func newBootstrapRunBudget() *bootstrapRunBudget {
	return &bootstrapRunBudget{
		roundsLeft:           businessProfileBootstrapMaxRounds,
		pageContentReadsLeft: businessProfileBootstrapMaxPageContentReads,
		contentPageURLs:      make(map[string]bool),
	}
}

type bootstrapPageRef struct {
	URL   string
	Title string
}

// handleBusinessProfileBootstrap creates a project's business profile without a
// chat turn: it writes no conversation messages and consumes no chat quota.
func (w *Worker) handleBusinessProfileBootstrap(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	if _, err := w.queries.GetProjectBusinessProfileByProjectID(ctx, job.ProjectID); err == nil {
		// Idempotent no-op: an existing profile is never overwritten. Return nil
		// so the worker's success finalization still advances the setup from
		// profile_generation to prompt_generation and enqueues the prompt job.
		// A retry after a mid-flight crash takes this same path.
		log.Printf("business profile bootstrap: project=%s already has a profile; no-op", job.ProjectID.String())
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check existing business profile: %w", err)
	}

	setup, err := w.queries.GetProjectSetupByProjectID(ctx, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load project setup for bootstrap: %w", err)
	}
	requester := setup.RequestedByUserID
	if !requester.Valid {
		return errors.New("project setup has no requested_by_user_id")
	}

	project, err := w.queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: job.ProjectID, UserID: requester})
	if err != nil {
		return fmt.Errorf("load project for bootstrap: %w", err)
	}

	registry := aichattools.NewRegistry()
	scope := aichattools.Scope{
		UserID:            requester,
		ProjectID:         job.ProjectID,
		Queries:           w.queries,
		DB:                w.pool,
		RowBudget:         aichattools.NewBudget(businessProfileBootstrapRowBudget),
		PageContentBudget: aichattools.NewPageContentBudget(businessProfileBootstrapPageBytes, businessProfileBootstrapPagePages),
		Web:               w.Web,
		WebBudget:         aichattools.NewWebBudget(businessProfileBootstrapWebSearches, businessProfileBootstrapWebFetches),
		// Setup chaining owns the prompt_generation enqueue once it advances the
		// setup; the tool must not race a second job in.
		SuppressPromptGeneration: true,
	}
	if crawlID, crawlErr := w.queries.GetLatestCompletedCrawlForProject(ctx, job.ProjectID); crawlErr == nil {
		scope.CrawlID = crawlID
	} else if !errors.Is(crawlErr, pgx.ErrNoRows) {
		return fmt.Errorf("load bootstrap crawl: %w", crawlErr)
	}

	model := strings.TrimSpace(w.cfg.DeepSeekModel)
	if model == "" {
		model = businessProfileBootstrapDefaultModel
	}
	streamer := ai.NewDeepSeekClient(w.cfg.DeepSeekAPIKey, model, w.cfg.DeepSeekBaseURL, nil)
	tools := businessProfileBootstrapToolDefs(registry)
	budget := newBootstrapRunBudget()

	messages := []ai.Message{
		{Role: ai.RoleSystem, Content: businessProfileBootstrapSystemPrompt},
		{Role: ai.RoleUser, Content: businessProfileBootstrapUserMessage(project.Name, project.BaseUrl, w.bootstrapPageRefs(ctx, scope))},
	}

	messages, err = w.runBusinessProfileBootstrapPass(ctx, streamer, model, tools, registry, scope, budget, executeBusinessProfileBootstrapTool, messages)
	if err == nil {
		return nil
	}
	log.Printf("business profile bootstrap: project=%s first pass failed: %v", job.ProjectID.String(), err)

	messages = append(messages, ai.Message{Role: ai.RoleUser, Content: businessProfileBootstrapCorrectionPrompt})
	if _, err = w.runBusinessProfileBootstrapPass(ctx, streamer, model, tools, registry, scope, budget, executeBusinessProfileBootstrapTool, messages); err != nil {
		return err
	}
	return nil
}

// bootstrapPageRefs lists crawl pages the model may read. It is best-effort:
// the bootstrap still runs (and can use web tools) when no crawl exists.
func (w *Worker) bootstrapPageRefs(ctx context.Context, scope aichattools.Scope) []bootstrapPageRef {
	if !scope.CrawlID.Valid {
		return nil
	}
	rows, err := w.queries.ListCrawlPageSummariesForCrawlByUser(ctx, sqlc.ListCrawlPageSummariesForCrawlByUserParams{
		CrawlID: scope.CrawlID,
		UserID:  scope.UserID,
		Limit:   businessProfileBootstrapPageListLimit,
		Offset:  0,
	})
	if err != nil {
		log.Printf("business profile bootstrap: page list for crawl %s failed: %v", scope.CrawlID.String(), err)
		return nil
	}
	refs := make([]bootstrapPageRef, 0, len(rows))
	for _, row := range rows {
		ref := bootstrapPageRef{URL: row.Url}
		if row.Title.Valid {
			ref.Title = row.Title.String
		}
		refs = append(refs, ref)
	}
	return refs
}

func businessProfileBootstrapUserMessage(projectName, baseURL string, pages []bootstrapPageRef) string {
	var builder strings.Builder
	builder.WriteString("Project name: ")
	builder.WriteString(projectName)
	builder.WriteString("\nProject base URL: ")
	builder.WriteString(baseURL)
	builder.WriteString("\n")
	if len(pages) == 0 {
		builder.WriteString("No crawl pages are available; use the base URL and web tools for evidence.\n")
		return builder.String()
	}
	builder.WriteString("\nCrawled pages you may read with read_page:\n")
	for _, page := range pages {
		builder.WriteString("- ")
		builder.WriteString(page.URL)
		if page.Title != "" {
			builder.WriteString(" (")
			builder.WriteString(page.Title)
			builder.WriteString(")")
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// runBusinessProfileBootstrapPass runs provider rounds until the model makes
// its one update call. It succeeds only when that call is the single
// update_business_profile call seen and it completed without an application
// error. A missing update or a page-content cap breach fails the pass so the
// caller can make its one corrective retry. All provider rounds and content
// reads come out of the shared budget, so the retry cannot double them. It
// returns the grown message history for that retry.
func (w *Worker) runBusinessProfileBootstrapPass(ctx context.Context, streamer ai.Streamer, model string, tools []ai.ToolDef, registry *aichattools.Registry, scope aichattools.Scope, budget *bootstrapRunBudget, exec bootstrapToolExecutor, messages []ai.Message) ([]ai.Message, error) {
	results := make([]bootstrapToolResult, 0, len(tools))
	for budget.roundsLeft > 0 {
		budget.roundsLeft--
		if err := ctx.Err(); err != nil {
			return messages, fmt.Errorf("business profile bootstrap interrupted: %w", err)
		}
		text, calls, err := streamBusinessProfileBootstrapRound(ctx, streamer, model, messages, tools)
		if err != nil {
			return messages, fmt.Errorf("business profile bootstrap provider: %w", err)
		}
		messages = append(messages, ai.Message{Role: ai.RoleAssistant, Content: text, ToolCalls: calls})
		if len(calls) == 0 {
			return messages, errors.New("business profile bootstrap produced assistant text without an update_business_profile call")
		}
		updateAttempted := false
		capBreached := false
		for _, call := range calls {
			var status string
			var result aichattools.Result
			if pageURL, isContent := businessProfileBootstrapPageContentReadURL(call); isContent {
				switch {
				case pageURL != "" && budget.contentPageURLs[pageURL]:
					status, result = "failed", aichattools.Result{Content: businessProfileBootstrapDuplicatePageMessage, Summary: "duplicate page content read"}
				case budget.pageContentReadsLeft <= 0:
					capBreached = true
					status, result = "failed", aichattools.Result{Content: "read_page content cap exceeded", Summary: "page content cap exceeded"}
				default:
					budget.pageContentReadsLeft--
					if pageURL != "" {
						budget.contentPageURLs[pageURL] = true
					}
					status, result = exec(ctx, registry, call, scope)
				}
			} else {
				status, result = exec(ctx, registry, call, scope)
			}
			result.Content = capBusinessProfileBootstrapToolResult(result.Content)
			if call.Name == businessProfileBootstrapUpdateTool {
				updateAttempted = true
			}
			results = append(results, bootstrapToolResult{Name: call.Name, Status: status})
			messages = append(messages, ai.Message{Role: ai.RoleTool, Content: result.Content, ToolCallID: call.ID, Name: call.Name})
		}
		if capBreached {
			return messages, fmt.Errorf("business profile bootstrap exceeded %d read_page content calls", businessProfileBootstrapMaxPageContentReads)
		}
		if businessProfileBootstrapUpdateSucceeded(results) {
			return messages, nil
		}
		if updateAttempted {
			return messages, errors.New("business profile bootstrap update_business_profile call did not succeed")
		}
	}
	return messages, fmt.Errorf("business profile bootstrap did not call update_business_profile within %d provider rounds", businessProfileBootstrapMaxRounds)
}

func streamBusinessProfileBootstrapRound(ctx context.Context, streamer ai.Streamer, model string, messages []ai.Message, tools []ai.ToolDef) (string, []ai.ToolCall, error) {
	var text strings.Builder
	var calls []ai.ToolCall
	err := streamer.Stream(ctx, ai.Request{
		Model:    model,
		Effort:   businessProfileBootstrapEffort,
		Messages: messages,
		Tools:    tools,
	}, func(event ai.Event) error {
		if event.ToolCall != nil {
			calls = append(calls, *event.ToolCall)
			return nil
		}
		if event.Text != "" {
			text.WriteString(event.Text)
		}
		return nil
	})
	return text.String(), calls, err
}

// businessProfileBootstrapToolDefs maps the strict allowlist to provider-facing
// definitions. Any tool absent from the allowlist is never offered.
func businessProfileBootstrapToolDefs(registry *aichattools.Registry) []ai.ToolDef {
	defs := make([]ai.ToolDef, 0, len(businessProfileBootstrapTools))
	for _, name := range businessProfileBootstrapTools {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		defs = append(defs, ai.ToolDef{Name: tool.Def.Name, Description: tool.Def.Description, Schema: tool.Def.Schema})
	}
	return defs
}

func isBusinessProfileBootstrapTool(name string) bool {
	for _, allowed := range businessProfileBootstrapTools {
		if allowed == name {
			return true
		}
	}
	return false
}

// businessProfileBootstrapPageContentReadURL reports whether a tool call is a
// read_page content read and returns its normalized crawl URL. Empty URL means
// the argument was missing or unparseable (the tool itself reports that).
func businessProfileBootstrapPageContentReadURL(call ai.ToolCall) (string, bool) {
	if call.Name != businessProfileBootstrapReadTool {
		return "", false
	}
	var args struct {
		URL  string `json:"url"`
		Mode string `json:"mode"`
	}
	if json.Unmarshal([]byte(call.Args), &args) != nil {
		return "", false
	}
	if args.Mode != "content" {
		return "", false
	}
	return normalizeBootstrapPageURL(args.URL), true
}

// normalizeBootstrapPageURL folds the harmless URL spellings the model may
// repeat (case, trailing slash, fragment) so duplicate content reads are
// caught. Query strings and paths stay significant.
func normalizeBootstrapPageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	if len(parsed.Path) > 1 {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}
	return parsed.String()
}

func capBusinessProfileBootstrapToolResult(content string) string {
	if len(content) <= businessProfileBootstrapToolResultCap {
		return content
	}
	return content[:businessProfileBootstrapToolResultCap] + "\u2026"
}

// bootstrapToolResult is one executed tool call reduced to the name and status
// the success rule needs.
type bootstrapToolResult struct {
	Name   string
	Status string
}

// businessProfileBootstrapUpdateSucceeded is the job's success rule: exactly one
// update_business_profile call must have been made, and it must have completed
// without an application error.
func businessProfileBootstrapUpdateSucceeded(results []bootstrapToolResult) bool {
	calls, succeeded := 0, 0
	for _, result := range results {
		if result.Name != businessProfileBootstrapUpdateTool {
			continue
		}
		calls++
		if result.Status == "completed" {
			succeeded++
		}
	}
	return calls == 1 && succeeded == 1
}

// executeBusinessProfileBootstrapTool runs one call through the registry and
// maps application-level errors ("<tool> error: ...") to a failed status, the
// same status semantics the chat loop uses. Calls outside the allowlist are
// rejected without touching the registry.
//
// The bootstrap validates update_business_profile arguments before running it:
// the chat tool accepts partial PATCHes, but this job must create one complete
// profile. Invalid arguments become a normal failed tool result so the model
// can correct them; the shared chat PATCH behavior is untouched.
func executeBusinessProfileBootstrapTool(ctx context.Context, registry *aichattools.Registry, call ai.ToolCall, scope aichattools.Scope) (string, aichattools.Result) {
	if !isBusinessProfileBootstrapTool(call.Name) {
		return "failed", aichattools.Result{Content: fmt.Sprintf("unknown tool %q", call.Name), Summary: "unknown tool"}
	}
	if call.Name == businessProfileBootstrapUpdateTool {
		if err := validateBusinessProfileBootstrapArgs(json.RawMessage(call.Args)); err != nil {
			message := call.Name + " error: " + err.Error()
			return "failed", aichattools.Result{Content: message, Summary: err.Error()}
		}
	}
	tool, ok := registry.Get(call.Name)
	if !ok {
		return "failed", aichattools.Result{Content: fmt.Sprintf("unknown tool %q", call.Name), Summary: "unknown tool"}
	}
	result, err := tool.Execute(ctx, json.RawMessage(call.Args), scope)
	if err != nil {
		return "failed", aichattools.Result{Content: err.Error(), Summary: "tool execution failed"}
	}
	if strings.HasPrefix(result.Content, call.Name+" error:") {
		return "failed", result
	}
	return "completed", result
}

// validateBusinessProfileBootstrapArgs enforces the complete-profile contract
// the bootstrap prompt promises: every scalar and array field present, no blank
// strings, no blank array entries, no placeholder tokens, non-empty arrays, and
// at most 5 seed_prompts. It deliberately does not validate or mutate anything
// the shared chat tool owns.
func validateBusinessProfileBootstrapArgs(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("arguments must be a JSON object")
	}
	known := make(map[string]bool, len(businessProfileBootstrapScalarFields)+len(businessProfileBootstrapArrayFields))
	for _, key := range businessProfileBootstrapScalarFields {
		known[key] = true
	}
	for _, key := range businessProfileBootstrapArrayFields {
		known[key] = true
	}
	for key := range fields {
		if !known[key] {
			return fmt.Errorf("unknown argument %q", key)
		}
	}

	for _, key := range businessProfileBootstrapScalarFields {
		value, ok := fields[key]
		if !ok {
			return fmt.Errorf("missing required argument %q", key)
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return fmt.Errorf("argument %q must be a string", key)
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("argument %q must not be blank", key)
		}
		if isBusinessProfileBootstrapPlaceholder(text) {
			return fmt.Errorf("argument %q must not be a placeholder value", key)
		}
	}

	for _, key := range businessProfileBootstrapArrayFields {
		value, ok := fields[key]
		if !ok {
			return fmt.Errorf("missing required argument %q", key)
		}
		var list []string
		if err := json.Unmarshal(value, &list); err != nil {
			return fmt.Errorf("argument %q must be an array of strings", key)
		}
		if len(list) == 0 {
			return fmt.Errorf("argument %q must not be empty", key)
		}
		if key == "seed_prompts" && len(list) > 5 {
			return fmt.Errorf("argument %q must have at most 5 values", key)
		}
		for _, item := range list {
			if strings.TrimSpace(item) == "" {
				return fmt.Errorf("argument %q must not contain blank values", key)
			}
			if isBusinessProfileBootstrapPlaceholder(item) {
				return fmt.Errorf("argument %q must not contain placeholder values", key)
			}
		}
	}
	return nil
}

func isBusinessProfileBootstrapPlaceholder(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unknown", "n/a", "n.a.", "tbd", "not specified":
		return true
	}
	return false
}
