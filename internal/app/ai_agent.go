package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/app/aitools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	// maxAgentToolRounds is a high safety backstop against a runaway loop, not a
	// working limit — the model is free to call tools as many times as it needs.
	// The only real per-turn cap is maxAgentPageReads (get_page_content).
	maxAgentToolRounds       = 40
	maxAgentReplayMessages   = 30
	maxAgentToolReplayLength = 4000
	// maxAgentPageReads caps get_page_content executions per turn, so a model
	// that greedily reads pages cannot balloon liveMessages past budget. Kept
	// low: each page can be 1-2k words, and fixes should lean on issue rows.
	maxAgentPageReads = 2
	// maxAIChatMessageBytes caps new user content before it reaches storage or a model.
	maxAIChatMessageBytes = 16 << 10
	// maxAgentReplayBytes caps prior message payloads sent to a model per turn.
	maxAgentReplayBytes = 128 << 10
	// maxAgentRequestBytes bounds all model input, including prompts, tools, and live tool results.
	maxAgentRequestBytes = 192 << 10
	agentMaxTokens       = 4096
	// agentTrimmedToolStub replaces an oldest live tool result's content when
	// this turn's cumulative tool output would otherwise overflow the request.
	agentTrimmedToolStub = "[earlier tool output omitted to fit context]"
)

var errAIRequestTooLarge = errors.New("AI request is too large")

// agentPersister is the narrow DB port the agent loop needs to persist
// messages, so tests can substitute a fake without a real database.
type agentPersister interface {
	CreateAIMessage(ctx context.Context, arg sqlc.CreateAIMessageParams) (sqlc.CreateAIMessageRow, error)
}

// agentToolRegistry is the narrow tool lookup port the agent loop needs.
type agentToolRegistry interface {
	Defs() []ai.ToolDef
	Get(name string) (aitools.Tool, bool)
}

// agentTurnParams is everything runAgentTurn needs to drive one user turn.
// History holds the conversation's prior persisted messages, in order, not
// including the new user turn; runAgentTurn appends the new user message.
type agentTurnParams struct {
	Client         ai.Client
	Registry       agentToolRegistry
	Queries        agentPersister
	ConversationID pgtype.UUID
	Scope          aitools.Scope
	SystemPrompt   string
	History        []sqlc.ListAIMessagesForConversationForUserRow
	UserContent    string
	SSE            *sseWriter
	MaxTokens      int
}

// sseDeltaPayload, sseToolCallPayload, sseToolResultPayload, sseDonePayload,
// and sseErrorPayload are the JSON bodies sent with each SSE event type.
type sseDeltaPayload struct {
	Delta string `json:"delta"`
}

type sseToolCallPayload struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type sseToolResultPayload struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type sseDonePayload struct {
	MessageIDs []string `json:"message_ids,omitempty"`
}

type sseErrorPayload struct {
	Message string `json:"message"`
}

type sseNavigatePayload struct {
	Destination string `json:"destination"`
}

type sseProjectSwitchedPayload struct {
	ProjectID string `json:"project_id"`
}

type sseCompareStartedPayload struct {
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id"`
}

type sseExportPayload struct {
	Kind      string `json:"kind"`
	Format    string `json:"format"`
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id,omitempty"`
}

type sseChartPayload struct {
	ID    string             `json:"id"`
	Chart *aitools.ChartSpec `json:"chart"`
}

// storedToolCall is the JSON shape persisted in ai_messages.tool_calls for an
// assistant row that invoked tools.
type storedToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// runAgentTurn drives one full user turn of the tool-calling agent loop:
// it streams model output to sse as it arrives, persists every assistant/tool
// message row, executes tool calls via registry, and loops until the model
// stops calling tools or the round cap is hit. It returns nil once a "done"
// SSE event has been sent (including the round-cap and stream-error cases,
// which are reported to the client over SSE rather than as a Go error).
func runAgentTurn(ctx context.Context, p agentTurnParams) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	turnStart := time.Now()
	convID := p.ConversationID.String()
	log.Printf("ai agent turn start conversation=%s user=%s", convID, p.Scope.UserID.String())

	historyGroups := cappedReplayGroups(replayMessageGroups(p.History))
	liveMessages := make([]ai.Message, 0, maxAgentToolRounds*2)
	pageReads := 0

	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = agentMaxTokens
	}

	for round := 0; round < maxAgentToolRounds; round++ {
		tools := p.Registry.Defs()
		messages, requestErr := boundedAgentMessages(p.SystemPrompt, historyGroups, p.UserContent, liveMessages, tools)
		if requestErr != nil {
			log.Printf("ai agent turn request rejected conversation=%s round=%d duration=%s", convID, round, time.Since(turnStart))
			if err := p.SSE.send("error", sseErrorPayload{Message: errAIRequestTooLarge.Error()}); err != nil {
				return err
			}
			return requestErr
		}
		events, err := p.Client.StreamTurn(ctx, ai.TurnRequest{
			Messages:  messages,
			Tools:     tools,
			MaxTokens: maxTokens,
		})
		if err != nil {
			log.Printf("ai agent turn failed conversation=%s round=%d duration=%s tools=%d error_type=%T err=%v", convID, round, time.Since(turnStart), len(tools), err, err)
			if sseErr := p.SSE.send("error", sseErrorPayload{Message: safeAgentProviderError(err)}); sseErr != nil {
				return sseErr
			}
			return err
		}

		reasoning, text, toolCalls, usage, streamErr := drainAgentStream(events, p.SSE)
		if streamErr != nil {
			log.Printf("ai agent turn failed conversation=%s round=%d duration=%s tools=%d error_type=%T err=%v", convID, round, time.Since(turnStart), len(tools), streamErr, streamErr)
			if sseErr := p.SSE.send("error", sseErrorPayload{Message: safeAgentProviderError(streamErr)}); sseErr != nil {
				return sseErr
			}
			return streamErr
		}

		if len(toolCalls) == 0 {
			assistantRow, err := persistAgentMessage(ctx, p.Queries, agentMessageParams{
				conversationID: p.ConversationID,
				role:           "assistant",
				content:        text,
				crawlID:        p.Scope.CrawlID,
				reasoning:      reasoning,
			})
			if err != nil {
				log.Printf("ai agent turn failed conversation=%s round=%d duration=%s err=%v", convID, round, time.Since(turnStart), err)
				if sseErr := p.SSE.send("error", sseErrorPayload{Message: "failed to save response"}); sseErr != nil {
					return sseErr
				}
				return err
			}
			log.Printf("ai agent turn done conversation=%s rounds=%d duration=%s usage=%+v", convID, round+1, time.Since(turnStart), usage)
			if err := p.SSE.send("done", sseDonePayload{MessageIDs: []string{assistantRow.ID.String()}}); err != nil {
				return err
			}
			return nil
		}

		toolCallsJSON, err := encodeStoredToolCalls(toolCalls)
		if err != nil {
			if sseErr := p.SSE.send("error", sseErrorPayload{Message: "failed to save response"}); sseErr != nil {
				return sseErr
			}
			return err
		}
		assistantRow, err := persistAgentMessage(ctx, p.Queries, agentMessageParams{
			conversationID: p.ConversationID,
			role:           "assistant",
			content:        text,
			crawlID:        p.Scope.CrawlID,
			reasoning:      reasoning,
			toolCallsJSON:  toolCallsJSON,
		})
		if err != nil {
			if sseErr := p.SSE.send("error", sseErrorPayload{Message: "failed to save response"}); sseErr != nil {
				return sseErr
			}
			return err
		}
		messageIDs := []string{assistantRow.ID.String()}
		liveMessages = append(liveMessages, ai.Message{Role: ai.RoleAssistant, Content: text, ToolCalls: toolCalls})

		for _, call := range toolCalls {
			// Cap get_page_content per turn: past the budget, stub the result
			// instead of executing, so a greedy read stampede can't balloon
			// liveMessages past the request budget and hard-fail the turn.
			if call.Name == "get_page_content" {
				pageReads++
				if pageReads > maxAgentPageReads {
					result := aitools.Result{
						Content: fmt.Sprintf(`{"error":"page-read limit for this turn reached (%d pages). Do not read more pages; synthesize your answer from the pages already read and the issue rows already fetched."}`, maxAgentPageReads),
						Summary: "page-read limit reached",
					}
					toolRow, err := persistAgentMessage(ctx, p.Queries, agentMessageParams{
						conversationID: p.ConversationID,
						role:           "tool",
						content:        result.Content,
						crawlID:        p.Scope.CrawlID,
						toolCallID:     call.ID,
						toolName:       call.Name,
					})
					if err != nil {
						if sseErr := p.SSE.send("error", sseErrorPayload{Message: "failed to save tool result"}); sseErr != nil {
							return sseErr
						}
						return err
					}
					messageIDs = append(messageIDs, toolRow.ID.String())
					if err := p.SSE.send("tool_result", sseToolResultPayload{ID: call.ID, Name: call.Name, Summary: result.Summary}); err != nil {
						return err
					}
					liveMessages = append(liveMessages, ai.Message{Role: ai.RoleTool, Content: truncateAIFixText(result.Content, maxAgentToolReplayLength), ToolCallID: call.ID, Name: call.Name})
					continue
				}
			}

			toolStart := time.Now()
			result, succeeded := executeAgentTool(ctx, p.Registry, call, p.Scope)
			log.Printf("ai agent tool call conversation=%s name=%s duration=%s failed=%t", convID, call.Name, time.Since(toolStart), !succeeded)

			// A successful switch_project must repoint the in-flight scope so
			// later tools in this same turn read the new project's data instead
			// of the previous project's. p is a value copy, so mutating p.Scope
			// only affects the remainder of this turn.
			if succeeded && result.ProjectID != "" {
				updateScopeForProjectSwitch(ctx, &p.Scope, result.ProjectID)
			}

			toolRow, err := persistAgentMessage(ctx, p.Queries, agentMessageParams{
				conversationID: p.ConversationID,
				role:           "tool",
				content:        result.Content,
				crawlID:        p.Scope.CrawlID,
				toolCallID:     call.ID,
				toolName:       call.Name,
			})
			if err != nil {
				if sseErr := p.SSE.send("error", sseErrorPayload{Message: "failed to save tool result"}); sseErr != nil {
					return sseErr
				}
				return err
			}
			messageIDs = append(messageIDs, toolRow.ID.String())
			if succeeded {
				if err := emitAgentToolAction(p.SSE, call.ID, result); err != nil {
					return err
				}
			}
			if err := p.SSE.send("tool_result", sseToolResultPayload{ID: call.ID, Name: call.Name, Summary: result.Summary}); err != nil {
				return err
			}
			// Truncate only the model-facing copy; the DB row above keeps full content.
			liveMessages = append(liveMessages, ai.Message{Role: ai.RoleTool, Content: truncateAIFixText(result.Content, maxAgentToolReplayLength), ToolCallID: call.ID, Name: call.Name})
		}
	}

	// Tool-call budget exhausted. Instead of failing the turn, make one final
	// tool-less call so the model must synthesize an answer from what it has
	// already gathered rather than dead-ending on "round limit reached".
	log.Printf("ai agent turn rounds exhausted, forcing synthesis conversation=%s rounds=%d duration=%s", convID, maxAgentToolRounds, time.Since(turnStart))
	messages, requestErr := boundedAgentMessages(p.SystemPrompt, historyGroups, p.UserContent, liveMessages, nil)
	if requestErr != nil {
		if err := p.SSE.send("error", sseErrorPayload{Message: errAIRequestTooLarge.Error()}); err != nil {
			return err
		}
		return requestErr
	}
	messages = append(messages, ai.Message{
		Role:    ai.RoleUser,
		Content: "You have reached the tool-call limit for this turn. Do not request any more tools. Answer now with concrete, ready-to-apply recommendations based only on what you have already gathered above. If key information is still missing, briefly say what it is and what you would check next.",
	})
	events, err := p.Client.StreamTurn(ctx, ai.TurnRequest{Messages: messages, MaxTokens: maxTokens})
	if err != nil {
		if sseErr := p.SSE.send("error", sseErrorPayload{Message: safeAgentProviderError(err)}); sseErr != nil {
			return sseErr
		}
		return err
	}
	reasoning, text, _, usage, streamErr := drainAgentStream(events, p.SSE)
	if streamErr != nil {
		if sseErr := p.SSE.send("error", sseErrorPayload{Message: safeAgentProviderError(streamErr)}); sseErr != nil {
			return sseErr
		}
		return streamErr
	}
	assistantRow, err := persistAgentMessage(ctx, p.Queries, agentMessageParams{
		conversationID: p.ConversationID,
		role:           "assistant",
		content:        text,
		crawlID:        p.Scope.CrawlID,
		reasoning:      reasoning,
	})
	if err != nil {
		if sseErr := p.SSE.send("error", sseErrorPayload{Message: "failed to save response"}); sseErr != nil {
			return sseErr
		}
		return err
	}
	log.Printf("ai agent turn done via synthesis conversation=%s rounds=%d duration=%s usage=%+v", convID, maxAgentToolRounds, time.Since(turnStart), usage)
	if err := p.SSE.send("done", sseDonePayload{MessageIDs: []string{assistantRow.ID.String()}}); err != nil {
		return err
	}
	return nil
}

// drainAgentStream forwards every event on events to sse as it arrives and
// accumulates the full reasoning text, full answer text, any tool calls, and
// the usage reported on EventDone, for the turn. It always drains the
// channel to completion (or until ctx cancellation unwinds the pump
// goroutine), per the ai.Client streaming contract.
func drainAgentStream(events <-chan ai.StreamEvent, sse *sseWriter) (reasoning string, text string, toolCalls []ai.ToolCall, usage *ai.Usage, err error) {
	var reasoningBuilder, textBuilder strings.Builder
	for event := range events {
		switch event.Type {
		case ai.EventReasoning:
			reasoningBuilder.WriteString(event.Delta)
			if err = sse.send("reasoning", sseDeltaPayload{Delta: event.Delta}); err != nil {
				return reasoningBuilder.String(), textBuilder.String(), toolCalls, usage, err
			}
		case ai.EventText:
			textBuilder.WriteString(event.Delta)
			if err = sse.send("text", sseDeltaPayload{Delta: event.Delta}); err != nil {
				return reasoningBuilder.String(), textBuilder.String(), toolCalls, usage, err
			}
		case ai.EventToolCall:
			if event.ToolCall == nil {
				return reasoningBuilder.String(), textBuilder.String(), toolCalls, usage, errors.New("AI service returned an invalid tool call")
			}
			toolCalls = append(toolCalls, *event.ToolCall)
			if err = sse.send("tool_call", sseToolCallPayload{ID: event.ToolCall.ID, Name: event.ToolCall.Name, Args: json.RawMessage(event.ToolCall.Args)}); err != nil {
				return reasoningBuilder.String(), textBuilder.String(), toolCalls, usage, err
			}
		case ai.EventError:
			err = event.Err
		case ai.EventDone:
			usage = event.Usage
		}
	}
	return reasoningBuilder.String(), textBuilder.String(), toolCalls, usage, err
}

// executeAgentTool runs one model-requested tool call against the registry.
// Unknown tool names and execution errors are turned into tool content the
// model can see and recover from, rather than aborting the turn.
// updateScopeForProjectSwitch repoints the tool scope at a just-switched
// project so subsequent tools in the same turn operate on it. The crawl is
// re-resolved to the new project's latest completed crawl (mirroring the
// frontend's default selection), or cleared when the project has none — so
// tools report "no crawl" rather than the previous project's stale data.
func updateScopeForProjectSwitch(ctx context.Context, scope *aitools.Scope, projectID string) {
	parsed, err := parseUUIDParam(projectID)
	if err != nil {
		return
	}
	scope.ProjectID = parsed
	scope.CrawlID = pgtype.UUID{}
	if scope.Queries == nil {
		return
	}
	crawls, err := scope.Queries.ListCrawlsForProject(ctx, sqlc.ListCrawlsForProjectParams{
		ProjectID: parsed,
		Column2:   "completed",
		Limit:     1,
		Offset:    0,
	})
	if err != nil || len(crawls) == 0 {
		return
	}
	scope.CrawlID = crawls[0].ID
}

func executeAgentTool(ctx context.Context, registry agentToolRegistry, call ai.ToolCall, scope aitools.Scope) (aitools.Result, bool) {
	tool, ok := registry.Get(call.Name)
	if !ok {
		message := fmt.Sprintf("error: unknown tool %q", call.Name)
		return aitools.Result{Content: message, Summary: message}, false
	}

	result, err := tool.Execute(ctx, json.RawMessage(call.Args), scope)
	if err != nil {
		message := fmt.Sprintf("error: %s", err.Error())
		return aitools.Result{Content: message, Summary: "tool failed: " + err.Error()}, false
	}
	return result, true
}

func emitAgentToolAction(sse *sseWriter, callID string, result aitools.Result) error {
	switch {
	case result.Chart != nil:
		return sse.send("chart", sseChartPayload{ID: callID, Chart: result.Chart})
	case result.ExportAction != nil:
		action := result.ExportAction
		return sse.send("export", sseExportPayload{Kind: action.Kind, Format: action.Format, ProjectID: action.ProjectID, CrawlID: action.CrawlID})
	case result.CrawlID != "" && result.CrawlProjectID != "":
		return sse.send("crawl_started", map[string]string{"id": result.CrawlID, "project_id": result.CrawlProjectID})
	case result.CompareProjectID != "" && result.CompareCrawlID != "":
		return sse.send("compare_started", sseCompareStartedPayload{ProjectID: result.CompareProjectID, CrawlID: result.CompareCrawlID})
	case result.Destination != "":
		return sse.send("navigate", sseNavigatePayload{Destination: result.Destination})
	case result.ProjectID != "":
		return sse.send("project_switched", sseProjectSwitchedPayload{ProjectID: result.ProjectID})
	}
	return nil
}

// agentMessageParams collects the optional fields for persistAgentMessage so
// callers only set the ones relevant to their row's role.
type agentMessageParams struct {
	conversationID pgtype.UUID
	role           string
	content        string
	crawlID        pgtype.UUID
	reasoning      string
	toolCallsJSON  []byte
	toolCallID     string
	toolName       string
}

func persistAgentMessage(ctx context.Context, queries agentPersister, p agentMessageParams) (sqlc.CreateAIMessageRow, error) {
	return queries.CreateAIMessage(ctx, sqlc.CreateAIMessageParams{
		ConversationID:   p.conversationID,
		Role:             p.role,
		Content:          p.content,
		CrawlID:          p.crawlID,
		ReasoningContent: aiNullableText(p.reasoning),
		ToolCalls:        p.toolCallsJSON,
		ToolCallID:       aiNullableText(p.toolCallID),
		ToolName:         aiNullableText(p.toolName),
	})
}

// encodeStoredToolCalls marshals reassembled tool calls for persistence in
// ai_messages.tool_calls.
func encodeStoredToolCalls(toolCalls []ai.ToolCall) ([]byte, error) {
	stored := make([]storedToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		stored = append(stored, storedToolCall{ID: call.ID, Name: call.Name, Args: json.RawMessage(call.Args)})
	}
	return json.Marshal(stored)
}

// decodeStoredToolCalls parses a persisted tool_calls JSONB value back into
// ai.ToolCall for replay into a new TurnRequest.
func decodeStoredToolCalls(raw []byte) []ai.ToolCall {
	if len(raw) == 0 {
		return nil
	}
	var stored []storedToolCall
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil
	}
	toolCalls := make([]ai.ToolCall, 0, len(stored))
	for _, call := range stored {
		toolCalls = append(toolCalls, ai.ToolCall{ID: call.ID, Name: call.Name, Args: string(call.Args)})
	}
	return toolCalls
}

// replayAIMessages maps persisted ai_messages rows into ai.Message history
// for a new TurnRequest, capped to the most recent maxAgentReplayMessages
// rows. reasoning_content is never replayed: it is display-only, and
// DeepSeek rejects/misbehaves if reasoning is fed back as input.
//
// It also filters out protocol-invalid tool messages:
//   - tool messages whose tool_call_id is empty (NULL in DB),
//   - tool messages orphaned by the tail-slice cap, i.e. where the
//     preceding assistant-with-tool_calls was truncated away.
//   - tool messages whose tool_call_id does not match any tool call
//     from the most recent assistant-with-tool_calls.
func replayAIMessages(rows []sqlc.ListAIMessagesForConversationForUserRow) []ai.Message {
	groups := cappedReplayGroups(replayMessageGroups(rows))
	messages := make([]ai.Message, 0, len(rows))
	for _, group := range groups {
		messages = append(messages, group...)
	}
	return messages
}

func cappedReplayGroups(groups [][]ai.Message) [][]ai.Message {
	selected := make([][]ai.Message, 0, len(groups))
	bytes, count := 0, 0
	for index := len(groups) - 1; index >= 0; index-- {
		group := groups[index]
		groupBytes := replayMessagesBytes(group)
		if count+len(group) > maxAgentReplayMessages || bytes+groupBytes > maxAgentReplayBytes {
			break // never split a tool-call exchange to satisfy a replay cap
		}
		selected = append(selected, group)
		count += len(group)
		bytes += groupBytes
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

// replayMessageGroups returns only complete provider-protocol units. An assistant
// tool-call message is included only with every matching contiguous tool result.
func replayMessageGroups(rows []sqlc.ListAIMessagesForConversationForUserRow) [][]ai.Message {
	groups := make([][]ai.Message, 0, len(rows))
	for index := 0; index < len(rows); index++ {
		row := rows[index]
		switch row.Role {
		case "user":
			groups = append(groups, []ai.Message{{Role: ai.RoleUser, Content: row.Content}})
		case "assistant":
			if len(row.ToolCalls) == 0 {
				groups = append(groups, []ai.Message{{Role: ai.RoleAssistant, Content: row.Content}})
				continue
			}
			calls := decodeStoredToolCalls(row.ToolCalls)
			if len(calls) == 0 {
				continue
			}
			expected := make(map[string]bool, len(calls))
			valid := true
			for _, call := range calls {
				if call.ID == "" || expected[call.ID] {
					valid = false
					break
				}
				expected[call.ID] = true
			}
			group := []ai.Message{{Role: ai.RoleAssistant, Content: row.Content, ToolCalls: calls}}
			next := index + 1
			for valid && next < len(rows) && rows[next].Role == "tool" {
				toolID := nullableTextString(rows[next].ToolCallID)
				if toolID == "" || !expected[toolID] {
					valid = false
					break
				}
				delete(expected, toolID)
				group = append(group, ai.Message{Role: ai.RoleTool, Content: truncateAIFixText(rows[next].Content, maxAgentToolReplayLength), ToolCallID: toolID, Name: nullableTextString(rows[next].ToolName)})
				next++
			}
			if valid && len(expected) == 0 {
				groups = append(groups, group)
				index = next - 1
			}
		}
	}
	return groups
}

func replayMessagesBytes(messages []ai.Message) int {
	bytes := 0
	for _, message := range messages {
		bytes += len(message.Content) + len(message.ToolCallID) + len(message.Name)
		for _, call := range message.ToolCalls {
			bytes += len(call.ID) + len(call.Name) + len(call.Args)
		}
	}
	return bytes
}

// boundedAgentMessages drops only whole oldest replay groups. The current
// system/user input and this turn's complete assistant-tool exchanges cannot
// be truncated, so they fail before a provider call if they alone exceed budget.
func boundedAgentMessages(systemPrompt string, historyGroups [][]ai.Message, userContent string, liveMessages []ai.Message, tools []ai.ToolDef) ([]ai.Message, error) {
	fixed := make([]ai.Message, 0, len(liveMessages)+2)
	fixed = append(fixed, ai.Message{Role: ai.RoleSystem, Content: systemPrompt})
	fixed = append(fixed, ai.Message{Role: ai.RoleUser, Content: userContent})
	fixed = append(fixed, liveMessages...)
	// If this turn's live tool output alone overflows, stub oldest tool results
	// first (append copied the structs, so the caller's liveMessages is left
	// intact). Assistant rows carry the tool_calls and DeepSeek requires every
	// tool_call to keep its matching tool result, so we only replace tool-message
	// content in place — never drop a message or touch an assistant row — which
	// preserves the assistant-tool_call↔tool-result pairing. We give up only if
	// the non-trimmable skeleton (system + user + assistant tool_calls) alone
	// still exceeds budget after every live tool result has been stubbed.
	// liveMessages begin at index 2 (after system and user).
	for i := 2; i < len(fixed) && agentRequestBytes(fixed, tools) > maxAgentRequestBytes; i++ {
		if fixed[i].Role == ai.RoleTool {
			fixed[i].Content = agentTrimmedToolStub
		}
	}
	if agentRequestBytes(fixed, tools) > maxAgentRequestBytes {
		return nil, errAIRequestTooLarge
	}
	selected := make([][]ai.Message, 0, len(historyGroups))
	used := agentRequestBytes(fixed, tools)
	for index := len(historyGroups) - 1; index >= 0; index-- {
		group := historyGroups[index]
		groupBytes := agentRequestBytes(group, nil)
		if used+groupBytes > maxAgentRequestBytes {
			break
		}
		selected = append(selected, group)
		used += groupBytes
	}
	messages := make([]ai.Message, 0, 2+len(liveMessages))
	messages = append(messages, fixed[0])
	for index := len(selected) - 1; index >= 0; index-- {
		messages = append(messages, selected[index]...)
	}
	messages = append(messages, fixed[1:]...)
	return messages, nil
}

func agentRequestBytes(messages []ai.Message, tools []ai.ToolDef) int {
	bytes := 0
	for _, message := range messages {
		bytes += len(message.Role) + len(message.Content) + len(message.ToolCallID) + len(message.Name)
		for _, call := range message.ToolCalls {
			bytes += len(call.ID) + len(call.Name) + len(call.Args)
		}
	}
	for _, tool := range tools {
		bytes += len(tool.Name) + len(tool.Description) + len(tool.Schema)
	}
	return bytes
}

// sseIdleWriteTimeout bounds the gap between two writes on an SSE turn. It is
// refreshed on every write (see sseWriter.send), so it never cuts off a long
// but actively-streaming turn; it only closes a connection that has gone
// truly silent (e.g. a vanished client the server can't otherwise detect).
const sseIdleWriteTimeout = 90 * time.Second

// sseWriter writes Server-Sent Events to an http.ResponseWriter, flushing
// after every event so the client sees output as it streams in.
type sseWriter struct {
	w          http.ResponseWriter
	flusher    http.Flusher
	controller *http.ResponseController
}

// newSSEWriter sends the SSE response headers and returns a writer for the
// event stream. It must be called before any other write to w.
//
// It also switches the connection off the server's single global
// http.Server.WriteTimeout deadline (which is sized for ordinary handlers and
// would kill an agentic turn partway through) onto a sliding per-write
// deadline refreshed in send: this still bounds how long a stalled write can
// hang, but no longer bounds the turn as a whole.
func newSSEWriter(w http.ResponseWriter) *sseWriter {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(sseIdleWriteTimeout))

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	sse := &sseWriter{w: w, flusher: flusher, controller: controller}
	sse.flush()
	return sse
}

// send writes one SSE event with a JSON-encoded payload and reports a client
// disconnect to its caller so the request context can be canceled immediately.
func (s *sseWriter) send(event string, payload any) error {
	_ = s.controller.SetWriteDeadline(time.Now().Add(sseIdleWriteTimeout))
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))
	n, err := s.w.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *sseWriter) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func safeAgentProviderError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "AI response timed out"
	}
	return "AI service unavailable"
}
