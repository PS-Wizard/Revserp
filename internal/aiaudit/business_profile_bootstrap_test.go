package aiaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
)

func TestBusinessProfileBootstrapPrompt(t *testing.T) {
	prompt := businessProfileBootstrapSystemPrompt
	for _, want := range []string{
		"owner already",
		"exactly one update_business_profile call",
		"Never leave a string or array empty",
		"up to 5",
		"Online",
		"Global",
		"infer",
		"Unknown",
		"N/A",
		"TBD",
		"Not specified",
		"get_score_summary",
		"read_issues",
		"read_issue_work",
		"read_page",
		"web_search",
		"fetch_url",
		"home",
		"about",
		"services",
		"products",
		"contact",
		"at most 5 read_page content calls",
		"Never read the same page twice",
		"brand_name", "website_url", "primary_category", "primary_location",
		"business_description", "product_description", "target_audience",
		"business_competitors", "branded_keywords", "non_branded_keywords",
		"target_keywords", "seed_prompts",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	lower := strings.ToLower(prompt)
	for _, gone := range []string{"omit that field", "leave that field empty", "unknown optional", "leave unknown"} {
		if strings.Contains(lower, gone) {
			t.Errorf("prompt still tells the model to leave fields empty: %q", gone)
		}
	}
	if strings.Contains(prompt, "\u2014") {
		t.Error("prompt still contains an em dash")
	}
}

func TestBusinessProfileBootstrapToolDefs(t *testing.T) {
	defs := businessProfileBootstrapToolDefs(aichattools.NewRegistry())
	got := make(map[string]bool, len(defs))
	for _, def := range defs {
		got[def.Name] = true
		if def.Description == "" || len(def.Schema) == 0 {
			t.Errorf("tool %s missing description or schema", def.Name)
		}
	}

	for _, name := range []string{
		"read_issues",
		"get_score_summary",
		"get_business_profile",
		"read_issue_work",
		"read_page",
		"web_search",
		"fetch_url",
		"update_business_profile",
	} {
		if !got[name] {
			t.Errorf("missing available tool %q", name)
		}
	}
	if len(defs) != 8 {
		t.Fatalf("exposed %d tools, want 8: %+v", len(defs), defs)
	}
	// get_search_console_data refreshes Google connection tokens and needs an
	// integration this worker does not wire; render_chart is presentation only.
	for _, name := range []string{"get_search_console_data", "render_chart"} {
		if got[name] {
			t.Errorf("tool %q must not be exposed", name)
		}
	}
	// update_business_profile is the only write tool; everything else is read.
	if !isBusinessProfileBootstrapTool("update_business_profile") {
		t.Error("update_business_profile must be allowed")
	}
	for _, name := range []string{"read_issues", "get_score_summary", "read_page"} {
		if !isBusinessProfileBootstrapTool(name) {
			t.Errorf("read tool %q must be allowed", name)
		}
	}
	if isBusinessProfileBootstrapTool("get_search_console_data") || isBusinessProfileBootstrapTool("render_chart") {
		t.Error("disallowed tool passed the allowlist")
	}
}

func TestBusinessProfileBootstrapUpdateSucceeded(t *testing.T) {
	cases := []struct {
		name    string
		results []bootstrapToolResult
		want    bool
	}{
		{"no calls", nil, false},
		{"read only", []bootstrapToolResult{{Name: "read_page", Status: "completed"}}, false},
		{"one success with reads", []bootstrapToolResult{
			{Name: "get_score_summary", Status: "completed"},
			{Name: "read_page", Status: "completed"},
			{Name: "update_business_profile", Status: "completed"},
		}, true},
		{"one failed update", []bootstrapToolResult{{Name: "update_business_profile", Status: "failed"}}, false},
		{"two successful updates", []bootstrapToolResult{
			{Name: "update_business_profile", Status: "completed"},
			{Name: "update_business_profile", Status: "completed"},
		}, false},
		{"success then failure", []bootstrapToolResult{
			{Name: "update_business_profile", Status: "completed"},
			{Name: "update_business_profile", Status: "failed"},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := businessProfileBootstrapUpdateSucceeded(tc.results); got != tc.want {
				t.Fatalf("businessProfileBootstrapUpdateSucceeded = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecuteBusinessProfileBootstrapToolRejectsUnrelated(t *testing.T) {
	status, result := executeBusinessProfileBootstrapTool(context.Background(), aichattools.NewRegistry(), ai.ToolCall{Name: "get_search_console_data"}, aichattools.Scope{})
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(result.Content, "unknown tool") {
		t.Fatalf("content = %q, want unknown tool", result.Content)
	}
}

func TestExecuteBusinessProfileBootstrapToolRejectsIncompleteUpdateArgs(t *testing.T) {
	status, result := executeBusinessProfileBootstrapTool(context.Background(), aichattools.NewRegistry(), ai.ToolCall{Name: "update_business_profile", Args: `{}`}, aichattools.Scope{})
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(result.Content, "missing required argument") {
		t.Fatalf("content = %q, want missing-argument validation error", result.Content)
	}
}

func validBootstrapProfileFields() map[string]any {
	return map[string]any{
		"brand_name":           "Acme",
		"website_url":          "https://acme.example",
		"primary_category":     "Widgets",
		"primary_location":     "Portland, OR",
		"business_description": "Sells trail widgets.",
		"product_description":  "Trail widgets and gear.",
		"target_audience":      "Hikers",
		"business_competitors": []string{"CorpA", "CorpB"},
		"branded_keywords":     []string{"acme"},
		"non_branded_keywords": []string{"trail widgets", "hiking gear"},
		"seed_prompts":         []string{"best trail widgets?", "where to buy hiking gear?"},
		"target_keywords":      []string{"trail widgets", "hiking gear"},
	}
}

func marshalBootstrapFields(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	return encoded
}

func TestValidateBusinessProfileBootstrapArgs(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr string
	}{
		{"valid complete", func(map[string]any) {}, ""},
		{"missing scalar", func(f map[string]any) { delete(f, "brand_name") }, `missing required argument "brand_name"`},
		{"missing array", func(f map[string]any) { delete(f, "seed_prompts") }, `missing required argument "seed_prompts"`},
		{"blank scalar", func(f map[string]any) { f["primary_category"] = "   " }, `argument "primary_category" must not be blank`},
		{"placeholder scalar", func(f map[string]any) { f["primary_location"] = "N/A" }, `argument "primary_location" must not be a placeholder value`},
		{"empty array", func(f map[string]any) { f["branded_keywords"] = []string{} }, `argument "branded_keywords" must not be empty`},
		{"blank array value", func(f map[string]any) { f["target_keywords"] = []string{"ok", "  "} }, `argument "target_keywords" must not contain blank values`},
		{"placeholder array value", func(f map[string]any) { f["business_competitors"] = []string{"Unknown"} }, `argument "business_competitors" must not contain placeholder values`},
		{"seed prompts too many", func(f map[string]any) { f["seed_prompts"] = []string{"1", "2", "3", "4", "5", "6"} }, `argument "seed_prompts" must have at most 5 values`},
		{"unknown field", func(f map[string]any) { f["hacked"] = "yes" }, `unknown argument "hacked"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := validBootstrapProfileFields()
			tc.mutate(fields)
			err := validateBusinessProfileBootstrapArgs(marshalBootstrapFields(t, fields))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}

	if err := validateBusinessProfileBootstrapArgs(json.RawMessage(`[]`)); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("non-object err = %v, want JSON object error", err)
	}
}

func TestBusinessProfileBootstrapPageReadDetectionAndNormalization(t *testing.T) {
	pageURL, ok := businessProfileBootstrapPageContentReadURL(ai.ToolCall{Name: "read_page", Args: `{"url":"https://A.example/about/","mode":"content"}`})
	if !ok || pageURL != "https://a.example/about" {
		t.Fatalf("content read = (%q, %v), want normalized https://a.example/about", pageURL, ok)
	}
	if _, ok := businessProfileBootstrapPageContentReadURL(ai.ToolCall{Name: "read_page", Args: `{"url":"https://a.example","mode":"metadata"}`}); ok {
		t.Error("metadata read counted as content")
	}
	if _, ok := businessProfileBootstrapPageContentReadURL(ai.ToolCall{Name: "get_score_summary", Args: `{}`}); ok {
		t.Error("non-read_page counted")
	}
	if normalizeBootstrapPageURL("https://a.example/") != "https://a.example/" {
		t.Error("root URL should keep its single slash")
	}
	if got := capBusinessProfileBootstrapToolResult(strings.Repeat("x", businessProfileBootstrapToolResultCap+50)); len(got) != businessProfileBootstrapToolResultCap+len("\u2026") {
		t.Error("tool result cap not applied")
	}
}

// repeatBootstrapStreamer emits one tool call per provider round, with a
// distinct call id so a returned history can be checked for balanced results.
type repeatBootstrapStreamer struct {
	call    ai.ToolCall
	efforts []string
	calls   int
}

func (s *repeatBootstrapStreamer) Stream(_ context.Context, req ai.Request, emit func(ai.Event) error) error {
	s.efforts = append(s.efforts, req.Effort)
	call := s.call
	call.ID = fmt.Sprintf("call-%d", s.calls)
	s.calls++
	return emit(ai.Event{ToolCall: &call})
}

// uniqueContentReadStreamer emits a content read for a new URL each round.
type uniqueContentReadStreamer struct {
	round   int
	efforts []string
}

func (s *uniqueContentReadStreamer) Stream(_ context.Context, req ai.Request, emit func(ai.Event) error) error {
	s.efforts = append(s.efforts, req.Effort)
	call := ai.ToolCall{
		ID:   fmt.Sprintf("call-%d", s.round),
		Name: "read_page",
		Args: fmt.Sprintf(`{"url":"https://a.example/page-%d","mode":"content"}`, s.round),
	}
	s.round++
	return emit(ai.Event{ToolCall: &call})
}

// scriptedBootstrapStreamer emits preset tool calls per round, then nothing.
type scriptedBootstrapStreamer struct {
	rounds [][]ai.ToolCall
	round  int
}

func (s *scriptedBootstrapStreamer) Stream(_ context.Context, _ ai.Request, emit func(ai.Event) error) error {
	if s.round >= len(s.rounds) {
		return nil
	}
	calls := s.rounds[s.round]
	s.round++
	for i := range calls {
		call := calls[i]
		if call.ID == "" {
			call.ID = fmt.Sprintf("call-%d-%d", s.round, i)
		}
		if err := emit(ai.Event{ToolCall: &call}); err != nil {
			return err
		}
	}
	return nil
}

type countingBootstrapExecutor struct {
	calls int
}

func (e *countingBootstrapExecutor) run(_ context.Context, _ *aichattools.Registry, _ ai.ToolCall, _ aichattools.Scope) (string, aichattools.Result) {
	e.calls++
	return "completed", aichattools.Result{Content: "ok", Summary: "ok"}
}

func TestBusinessProfileBootstrapPageContentCap(t *testing.T) {
	streamer := &uniqueContentReadStreamer{}
	exec := &countingBootstrapExecutor{}
	budget := newBootstrapRunBudget()
	messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

	returned, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), streamer, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages)
	if err == nil || !strings.Contains(err.Error(), "exceeded 5 read_page content calls") {
		t.Fatalf("err = %v, want page-content cap breach", err)
	}
	if exec.calls != businessProfileBootstrapMaxPageContentReads {
		t.Fatalf("executed %d content reads, want %d", exec.calls, businessProfileBootstrapMaxPageContentReads)
	}
	if budget.pageContentReadsLeft != 0 {
		t.Fatalf("pageContentReadsLeft = %d, want 0", budget.pageContentReadsLeft)
	}
	for _, effort := range streamer.efforts {
		if effort != businessProfileBootstrapEffort {
			t.Fatalf("effort = %q, want %q", effort, businessProfileBootstrapEffort)
		}
	}
	assertBalancedBootstrapHistory(t, returned)
}

func TestBusinessProfileBootstrapRoundCap(t *testing.T) {
	streamer := &repeatBootstrapStreamer{call: ai.ToolCall{
		Name: "read_page",
		Args: `{"url":"https://a.example","mode":"metadata"}`,
	}}
	exec := &countingBootstrapExecutor{}
	budget := newBootstrapRunBudget()
	messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

	_, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), streamer, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages)
	if err == nil || !strings.Contains(err.Error(), "within 8 provider rounds") {
		t.Fatalf("err = %v, want tool-round cap breach", err)
	}
	if exec.calls != businessProfileBootstrapMaxRounds {
		t.Fatalf("executed %d rounds, want %d", exec.calls, businessProfileBootstrapMaxRounds)
	}
	if budget.roundsLeft != 0 {
		t.Fatalf("roundsLeft = %d, want 0", budget.roundsLeft)
	}
}

func TestBusinessProfileBootstrapDuplicateContentRead(t *testing.T) {
	read := ai.ToolCall{Name: "read_page", Args: `{"url":"https://a.example/about","mode":"content"}`}
	streamer := &scriptedBootstrapStreamer{rounds: [][]ai.ToolCall{{read}, {read}}}
	exec := &countingBootstrapExecutor{}
	budget := newBootstrapRunBudget()
	messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

	returned, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), streamer, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages)
	if err == nil || !strings.Contains(err.Error(), "without an update_business_profile call") {
		t.Fatalf("err = %v, want text-only failure after rounds", err)
	}
	if exec.calls != 1 {
		t.Fatalf("executed %d reads, want the duplicate rejected (1)", exec.calls)
	}
	if budget.pageContentReadsLeft != businessProfileBootstrapMaxPageContentReads-1 {
		t.Fatalf("pageContentReadsLeft = %d, want %d", budget.pageContentReadsLeft, businessProfileBootstrapMaxPageContentReads-1)
	}
	if !historyContainsToolContent(returned, "already read this page") {
		t.Fatalf("duplicate rejection message missing from history")
	}
	assertBalancedBootstrapHistory(t, returned)
}

func TestBusinessProfileBootstrapBudgetSharedAcrossAttempts(t *testing.T) {
	t.Run("page content reads", func(t *testing.T) {
		budget := newBootstrapRunBudget()
		exec := &countingBootstrapExecutor{}
		first := &uniqueContentReadStreamer{}
		messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}

		if _, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), first, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages); err == nil || !strings.Contains(err.Error(), "exceeded 5 read_page content calls") {
			t.Fatalf("first attempt err = %v, want cap breach", err)
		}
		if exec.calls != businessProfileBootstrapMaxPageContentReads {
			t.Fatalf("first attempt executed %d reads, want %d", exec.calls, businessProfileBootstrapMaxPageContentReads)
		}

		second := &uniqueContentReadStreamer{round: 100}
		if _, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), second, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages); err == nil || !strings.Contains(err.Error(), "exceeded 5 read_page content calls") {
			t.Fatalf("corrective attempt err = %v, want cap breach", err)
		}
		if exec.calls != businessProfileBootstrapMaxPageContentReads {
			t.Fatalf("total reads = %d, want %d shared across attempts", exec.calls, businessProfileBootstrapMaxPageContentReads)
		}
	})

	t.Run("provider rounds", func(t *testing.T) {
		budget := newBootstrapRunBudget()
		exec := &countingBootstrapExecutor{}
		messages := []ai.Message{{Role: ai.RoleSystem, Content: "system"}}
		newStreamer := func() *repeatBootstrapStreamer {
			return &repeatBootstrapStreamer{call: ai.ToolCall{Name: "read_page", Args: `{"url":"https://a.example","mode":"metadata"}`}}
		}

		if _, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), newStreamer(), "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages); err == nil || !strings.Contains(err.Error(), "within 8 provider rounds") {
			t.Fatalf("first attempt err = %v, want round cap", err)
		}
		if exec.calls != businessProfileBootstrapMaxRounds {
			t.Fatalf("first attempt executed %d rounds, want %d", exec.calls, businessProfileBootstrapMaxRounds)
		}

		if _, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), newStreamer(), "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, messages); err == nil || !strings.Contains(err.Error(), "within 8 provider rounds") {
			t.Fatalf("corrective attempt err = %v, want round cap", err)
		}
		if exec.calls != businessProfileBootstrapMaxRounds {
			t.Fatalf("total rounds = %d, want %d shared across attempts", exec.calls, businessProfileBootstrapMaxRounds)
		}
	})
}

type textOnlyBootstrapStreamer struct{}

func (textOnlyBootstrapStreamer) Stream(_ context.Context, _ ai.Request, emit func(ai.Event) error) error {
	return emit(ai.Event{Text: "Here is the profile."})
}

func TestBusinessProfileBootstrapAssistantTextFails(t *testing.T) {
	exec := &countingBootstrapExecutor{}
	budget := newBootstrapRunBudget()

	_, err := (&Worker{}).runBusinessProfileBootstrapPass(context.Background(), textOnlyBootstrapStreamer{}, "deepseek-flash", nil, aichattools.NewRegistry(), aichattools.Scope{}, budget, exec.run, []ai.Message{{Role: ai.RoleSystem, Content: "system"}})
	if err == nil || !strings.Contains(err.Error(), "without an update_business_profile call") {
		t.Fatalf("err = %v, want text-only failure", err)
	}
}

// assertBalancedBootstrapHistory requires every assistant tool call to have a
// matching tool result, so a corrective retry gets a provider-valid history.
func assertBalancedBootstrapHistory(t *testing.T, messages []ai.Message) {
	t.Helper()
	pending := make(map[string]bool)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			pending[call.ID] = true
		}
		if message.Role == ai.RoleTool {
			if !pending[message.ToolCallID] {
				t.Fatalf("tool result %q has no matching call", message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatalf("tool calls without results: %v", pending)
	}
}

func historyContainsToolContent(messages []ai.Message, want string) bool {
	for _, message := range messages {
		if message.Role == ai.RoleTool && strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}
