package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/app/aitools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// scriptedClient is a fake ai.Client that replays a fixed sequence of turns,
// one []ai.StreamEvent per call to StreamTurn.
type scriptedClient struct {
	turns   [][]ai.StreamEvent
	calls   int
	sentReq []ai.TurnRequest
}

func (c *scriptedClient) StreamTurn(_ context.Context, req ai.TurnRequest) (<-chan ai.StreamEvent, error) {
	c.sentReq = append(c.sentReq, req)
	if c.calls >= len(c.turns) {
		return nil, errors.New("scriptedClient: no more scripted turns")
	}
	events := c.turns[c.calls]
	c.calls++
	ch := make(chan ai.StreamEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (c *scriptedClient) GenerateText(_ context.Context, _ string) (string, error) {
	return "", nil
}

// fakeRegistry is a minimal agentToolRegistry backed by an in-memory map.
type fakeRegistry struct {
	tools map[string]aitools.Tool
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{tools: make(map[string]aitools.Tool)}
}

func (r *fakeRegistry) register(name string, exec func(ctx context.Context, args json.RawMessage, s aitools.Scope) (aitools.Result, error)) {
	r.tools[name] = aitools.Tool{
		Def:     ai.ToolDef{Name: name, Description: name, Schema: json.RawMessage(`{"type":"object"}`)},
		Execute: exec,
	}
}

func (r *fakeRegistry) Defs() []ai.ToolDef {
	defs := make([]ai.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Def)
	}
	return defs
}

func (r *fakeRegistry) Get(name string) (aitools.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// fakePersister is an in-memory agentPersister that records every row
// CreateAIMessage was called with, in order.
type fakePersister struct {
	rows   []sqlc.CreateAIMessageParams
	nextID byte
}

func (f *fakePersister) CreateAIMessage(_ context.Context, arg sqlc.CreateAIMessageParams) (sqlc.CreateAIMessageRow, error) {
	f.nextID++
	f.rows = append(f.rows, arg)
	return sqlc.CreateAIMessageRow{
		ID:               fakeUUID(f.nextID),
		ConversationID:   arg.ConversationID,
		Role:             arg.Role,
		Content:          arg.Content,
		CrawlID:          arg.CrawlID,
		ReasoningContent: arg.ReasoningContent,
		ToolCalls:        arg.ToolCalls,
		ToolCallID:       arg.ToolCallID,
		ToolName:         arg.ToolName,
	}, nil
}

func fakeUUID(n byte) pgtype.UUID {
	var u pgtype.UUID
	u.Valid = true
	u.Bytes[15] = n
	return u
}

// recordedSSEEvent is one parsed "event: ...\ndata: ...\n\n" block.
type recordedSSEEvent struct {
	Event string
	Data  string
}

func parseSSEEvents(t *testing.T, body string) []recordedSSEEvent {
	t.Helper()
	var events []recordedSSEEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var rec recordedSSEEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				rec.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				rec.Data = strings.TrimPrefix(line, "data: ")
			}
		}
		events = append(events, rec)
	}
	return events
}

func newTestSSE() (*sseWriter, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	return newSSEWriter(recorder), recorder
}

func TestRunAgentTurn_TextOnly(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{
		{
			{Type: ai.EventReasoning, Delta: "thinking"},
			{Type: ai.EventText, Delta: "Hello "},
			{Type: ai.EventText, Delta: "world"},
			{Type: ai.EventDone},
		},
	}}
	registry := newFakeRegistry()
	persister := &fakePersister{}
	sse, recorder := newTestSSE()

	err := runAgentTurn(context.Background(), agentTurnParams{
		Client:         client,
		Registry:       registry,
		Queries:        persister,
		ConversationID: fakeUUID(1),
		Scope:          aitools.Scope{CrawlID: fakeUUID(2)},
		SystemPrompt:   "system",
		UserContent:    "hi",
		SSE:            sse,
	})
	if err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}

	if len(persister.rows) != 1 {
		t.Fatalf("expected 1 persisted row, got %d", len(persister.rows))
	}
	assistantRow := persister.rows[0]
	if assistantRow.Role != "assistant" || assistantRow.Content != "Hello world" {
		t.Errorf("unexpected assistant row: %+v", assistantRow)
	}
	if assistantRow.ReasoningContent.String != "thinking" {
		t.Errorf("expected reasoning persisted, got %q", assistantRow.ReasoningContent.String)
	}
	if len(assistantRow.ToolCalls) != 0 {
		t.Errorf("expected no tool_calls on a text-only turn, got %s", assistantRow.ToolCalls)
	}

	events := parseSSEEvents(t, recorder.Body.String())
	wantTypes := []string{"reasoning", "text", "text", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("expected %d SSE events, got %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Event != want {
			t.Errorf("event %d: expected %q, got %q", i, want, events[i].Event)
		}
	}
}

func TestRunAgentTurn_ToolCallsThenAnswer(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{
		{
			{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call_1", Name: "get_score_summary", Args: `{}`}},
			{Type: ai.EventDone},
		},
		{
			{Type: ai.EventText, Delta: "Final answer"},
			{Type: ai.EventDone},
		},
	}}
	registry := newFakeRegistry()
	registry.register("get_score_summary", func(_ context.Context, _ json.RawMessage, _ aitools.Scope) (aitools.Result, error) {
		return aitools.Result{Content: `{"overall":80}`, Summary: "score summary loaded"}, nil
	})
	persister := &fakePersister{}
	sse, recorder := newTestSSE()

	err := runAgentTurn(context.Background(), agentTurnParams{
		Client:         client,
		Registry:       registry,
		Queries:        persister,
		ConversationID: fakeUUID(1),
		Scope:          aitools.Scope{CrawlID: fakeUUID(2)},
		SystemPrompt:   "system",
		UserContent:    "how am I doing?",
		SSE:            sse,
	})
	if err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}

	if len(persister.rows) != 3 {
		t.Fatalf("expected 3 persisted rows (assistant+tool+assistant), got %d", len(persister.rows))
	}
	if persister.rows[0].Role != "assistant" || len(persister.rows[0].ToolCalls) == 0 {
		t.Errorf("expected first row to be an assistant row with tool_calls, got %+v", persister.rows[0])
	}
	if persister.rows[1].Role != "tool" || persister.rows[1].Content != `{"overall":80}` {
		t.Errorf("unexpected tool row: %+v", persister.rows[1])
	}
	if persister.rows[1].ToolCallID.String != "call_1" || persister.rows[1].ToolName.String != "get_score_summary" {
		t.Errorf("tool row missing call linkage: %+v", persister.rows[1])
	}
	if persister.rows[2].Role != "assistant" || persister.rows[2].Content != "Final answer" {
		t.Errorf("unexpected final assistant row: %+v", persister.rows[2])
	}

	events := parseSSEEvents(t, recorder.Body.String())
	wantTypes := []string{"tool_call", "tool_result", "text", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("expected %d SSE events, got %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Event != want {
			t.Errorf("event %d: expected %q, got %q", i, want, events[i].Event)
		}
	}

	// The second StreamTurn call must have seen the tool's result appended.
	if len(client.sentReq) != 2 {
		t.Fatalf("expected 2 StreamTurn calls, got %d", len(client.sentReq))
	}
	secondReqMessages := client.sentReq[1].Messages
	foundToolMessage := false
	for _, m := range secondReqMessages {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_1" {
			foundToolMessage = true
		}
	}
	if !foundToolMessage {
		t.Error("expected the second turn's request to include the tool result message")
	}
}

func TestRunAgentTurn_ToolExecuteError(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{
		{
			{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call_1", Name: "broken_tool", Args: `{}`}},
			{Type: ai.EventDone},
		},
		{
			{Type: ai.EventText, Delta: "recovered"},
			{Type: ai.EventDone},
		},
	}}
	registry := newFakeRegistry()
	registry.register("broken_tool", func(_ context.Context, _ json.RawMessage, _ aitools.Scope) (aitools.Result, error) {
		return aitools.Result{}, errors.New("boom")
	})
	persister := &fakePersister{}
	sse, recorder := newTestSSE()

	err := runAgentTurn(context.Background(), agentTurnParams{
		Client:         client,
		Registry:       registry,
		Queries:        persister,
		ConversationID: fakeUUID(1),
		Scope:          aitools.Scope{CrawlID: fakeUUID(2)},
		SystemPrompt:   "system",
		UserContent:    "hi",
		SSE:            sse,
	})
	if err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}

	toolRow := persister.rows[1]
	if !strings.Contains(toolRow.Content, "boom") {
		t.Errorf("expected tool row content to surface the error, got %q", toolRow.Content)
	}

	events := parseSSEEvents(t, recorder.Body.String())
	foundToolResult := false
	for _, e := range events {
		if e.Event == "tool_result" && strings.Contains(e.Data, "tool failed") {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Errorf("expected a tool_result event reporting the failure, got %+v", events)
	}
}

// ctxAwareClient is an ai.Client whose StreamTurn starts a goroutine that
// keeps sending events on an unbuffered-relative-to-volume channel until ctx
// is canceled, mirroring the real StreamTurn contract: the producer must
// unwind on ctx cancellation rather than block forever on a channel send
// nobody is draining. done is closed once the goroutine has exited, so tests
// can assert it doesn't leak.
type ctxAwareClient struct {
	done chan struct{}
}

func (c *ctxAwareClient) StreamTurn(ctx context.Context, _ ai.TurnRequest) (<-chan ai.StreamEvent, error) {
	events := make(chan ai.StreamEvent, 8)
	go func() {
		defer close(events)
		defer close(c.done)
		for {
			select {
			case <-ctx.Done():
				return
			case events <- (ai.StreamEvent{Type: ai.EventText, Delta: "x"}):
			}
		}
	}()
	return events, nil
}

func (c *ctxAwareClient) GenerateText(_ context.Context, _ string) (string, error) {
	return "", nil
}

// TestRunAgentTurn_ContextCancelUnwindsPump asserts that abandoning a turn
// (e.g. the client disconnecting mid-stream, which cancels the request
// context) unwinds both runAgentTurn and the stream producer goroutine,
// rather than leaving the producer blocked forever on a channel send. A
// leaked producer here would mean a leaked goroutine on every abandoned
// turn, which over time exhausts app resources for unrelated requests.
func TestRunAgentTurn_ContextCancelUnwindsPump(t *testing.T) {
	client := &ctxAwareClient{done: make(chan struct{})}
	registry := newFakeRegistry()
	persister := &fakePersister{}
	sse, _ := newTestSSE()

	ctx, cancel := context.WithCancel(context.Background())

	turnDone := make(chan error, 1)
	go func() {
		turnDone <- runAgentTurn(ctx, agentTurnParams{
			Client:         client,
			Registry:       registry,
			Queries:        persister,
			ConversationID: fakeUUID(1),
			Scope:          aitools.Scope{CrawlID: fakeUUID(2)},
			SystemPrompt:   "system",
			UserContent:    "hi",
			SSE:            sse,
		})
	}()

	// Let a handful of events flow before abandoning the turn, as if the
	// client disconnected partway through a real stream.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("stream producer goroutine did not unwind after ctx cancellation")
	}

	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("runAgentTurn did not return after ctx cancellation")
	}
}

func TestRunAgentTurn_RoundCapReached(t *testing.T) {
	toolCallTurn := []ai.StreamEvent{
		{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call_x", Name: "loop_tool", Args: `{}`}},
		{Type: ai.EventDone},
	}
	turns := make([][]ai.StreamEvent, 0, maxAgentToolRounds)
	for i := 0; i < maxAgentToolRounds; i++ {
		turns = append(turns, toolCallTurn)
	}
	client := &scriptedClient{turns: turns}
	registry := newFakeRegistry()
	registry.register("loop_tool", func(_ context.Context, _ json.RawMessage, _ aitools.Scope) (aitools.Result, error) {
		return aitools.Result{Content: "ok", Summary: "ok"}, nil
	})
	persister := &fakePersister{}
	sse, recorder := newTestSSE()

	err := runAgentTurn(context.Background(), agentTurnParams{
		Client:         client,
		Registry:       registry,
		Queries:        persister,
		ConversationID: fakeUUID(1),
		Scope:          aitools.Scope{CrawlID: fakeUUID(2)},
		SystemPrompt:   "system",
		UserContent:    "loop forever",
		SSE:            sse,
	})
	if err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}
	if client.calls != maxAgentToolRounds {
		t.Errorf("expected exactly %d StreamTurn calls, got %d", maxAgentToolRounds, client.calls)
	}

	events := parseSSEEvents(t, recorder.Body.String())
	if len(events) == 0 || events[len(events)-1].Event != "done" {
		t.Fatalf("expected the last event to be done, got %+v", events)
	}
	foundLimitError := false
	for _, e := range events {
		if e.Event == "error" && strings.Contains(e.Data, "tool round limit reached") {
			foundLimitError = true
		}
	}
	if !foundLimitError {
		t.Errorf("expected a round-limit error event, got %+v", events)
	}
}

func TestReplayAIMessages_ValidSequencePreserved(t *testing.T) {
	rows := []sqlc.ListAIMessagesForConversationForUserRow{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"get_score","args":"{}"}]`)},
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_1", Valid: true}, ToolName: pgtype.Text{String: "get_score", Valid: true}, Content: "42"},
		{Role: "assistant", Content: "done"},
	}
	msgs := replayAIMessages(rows)
	want := 4
	if len(msgs) != want {
		t.Fatalf("expected %d messages, got %d", want, len(msgs))
	}
	if msgs[0].Role != ai.RoleUser || msgs[0].Content != "hello" {
		t.Errorf("first message unexpected: %+v", msgs[0])
	}
	if msgs[1].Role != ai.RoleAssistant || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "call_1" {
		t.Errorf("assistant with tool_calls unexpected: %+v", msgs[1])
	}
	if msgs[2].Role != ai.RoleTool || msgs[2].ToolCallID != "call_1" {
		t.Errorf("tool message unexpected: %+v", msgs[2])
	}
	if msgs[3].Role != ai.RoleAssistant || len(msgs[3].ToolCalls) != 0 {
		t.Errorf("final assistant unexpected: %+v", msgs[3])
	}
}

func TestReplayAIMessages_TruncationOrphansTool(t *testing.T) {
	// Build 31 rows: 1 assistant-with-tool_calls + 30 user messages.
	// The 30-row cap drops the assistant, leaving the tool orphaned.
	rows := make([]sqlc.ListAIMessagesForConversationForUserRow, 31)
	rows[0] = sqlc.ListAIMessagesForConversationForUserRow{
		Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"t","args":"{}"}]`),
	}
	rows[1] = sqlc.ListAIMessagesForConversationForUserRow{
		Role: "tool", ToolCallID: pgtype.Text{String: "call_1", Valid: true}, ToolName: pgtype.Text{String: "t", Valid: true}, Content: "result",
	}
	for i := 2; i < 31; i++ {
		rows[i] = sqlc.ListAIMessagesForConversationForUserRow{Role: "user", Content: "msg"}
	}
	msgs := replayAIMessages(rows)
	// After truncation: tool(id=call_1) + 29 user messages (30 total).
	// The orphaned tool must be dropped because no assistant-with-tool_calls precedes it.
	if len(msgs) != 29 {
		t.Fatalf("expected 29 messages (orphaned tool dropped), got %d", len(msgs))
	}
	for i, m := range msgs {
		if m.Role == ai.RoleTool {
			t.Errorf("message %d is a tool message, should have been dropped: %+v", i, m)
		}
	}
}

func TestReplayAIMessages_EmptyToolCallID(t *testing.T) {
	rows := []sqlc.ListAIMessagesForConversationForUserRow{
		{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"t","args":"{}"}]`)},
		// ToolCallID is NULL / invalid
		{Role: "tool", ToolCallID: pgtype.Text{Valid: false}, ToolName: pgtype.Text{String: "t", Valid: true}, Content: "result"},
	}
	msgs := replayAIMessages(rows)
	if len(msgs) != 0 {
		t.Fatalf("expected incomplete tool exchange to be dropped, got %d messages", len(msgs))
	}
}

func TestReplayAIMessages_NoMatchingToolCallID(t *testing.T) {
	rows := []sqlc.ListAIMessagesForConversationForUserRow{
		{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"t","args":"{}"}]`)},
		// tool_call_id does not match any call from the assistant
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_999", Valid: true}, ToolName: pgtype.Text{String: "t", Valid: true}, Content: "result"},
	}
	msgs := replayAIMessages(rows)
	if len(msgs) != 0 {
		t.Fatalf("expected incomplete tool exchange to be dropped, got %d messages", len(msgs))
	}
}

func TestReplayAIMessages_NonToolAssistantResets(t *testing.T) {
	rows := []sqlc.ListAIMessagesForConversationForUserRow{
		{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"t","args":"{}"}]`)},
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_1", Valid: true}, ToolName: pgtype.Text{String: "t", Valid: true}, Content: "result"},
		// Non-tool-calling assistant resets pending set
		{Role: "assistant", Content: "ok"},
		// This tool has no preceding assistant-with-tool_calls anymore
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_1", Valid: true}, ToolName: pgtype.Text{String: "t", Valid: true}, Content: "orphan"},
	}
	msgs := replayAIMessages(rows)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (assistant+tool+assistant), got %d", len(msgs))
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// Third message should be the second assistant, not the orphaned tool
	if msgs[2].Role != ai.RoleAssistant || msgs[2].Content != "ok" {
		t.Errorf("expected final assistant (tool was dropped), got %+v", msgs[2])
	}
}

func TestReplayAIMessages_MultipleToolResults(t *testing.T) {
	rows := []sqlc.ListAIMessagesForConversationForUserRow{
		{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"a","args":"{}"},{"id":"call_2","name":"b","args":"{}"}]`)},
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_1", Valid: true}, ToolName: pgtype.Text{String: "a", Valid: true}, Content: "r1"},
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_2", Valid: true}, ToolName: pgtype.Text{String: "b", Valid: true}, Content: "r2"},
	}
	msgs := replayAIMessages(rows)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (assistant+2 tools), got %d", len(msgs))
	}
	if msgs[0].Role != ai.RoleAssistant || len(msgs[0].ToolCalls) != 2 {
		t.Errorf("expected assistant with 2 tool calls, got %+v", msgs[0])
	}
	if msgs[1].Role != ai.RoleTool || msgs[1].ToolCallID != "call_1" {
		t.Errorf("expected first tool result, got %+v", msgs[1])
	}
	if msgs[2].Role != ai.RoleTool || msgs[2].ToolCallID != "call_2" {
		t.Errorf("expected second tool result, got %+v", msgs[2])
	}
}

func TestReplayAIMessages_ByteCapKeepsCompleteToolExchange(t *testing.T) {
	rows := []sqlc.ListAIMessagesForConversationForUserRow{
		{Role: "user", Content: strings.Repeat("old", maxAgentReplayBytes/3)},
		{Role: "assistant", ToolCalls: []byte(`[{"id":"call_1","name":"t","args":"{}"}]`)},
		{Role: "tool", ToolCallID: pgtype.Text{String: "call_1", Valid: true}, Content: "result"},
		{Role: "assistant", Content: "latest"},
	}
	msgs := replayAIMessages(rows)
	if len(msgs) != 3 {
		t.Fatalf("expected complete recent exchange and latest answer, got %d messages", len(msgs))
	}
	if msgs[0].Role != ai.RoleAssistant || len(msgs[0].ToolCalls) != 1 || msgs[1].Role != ai.RoleTool || msgs[2].Content != "latest" {
		t.Fatalf("replay split or reordered protocol messages: %+v", msgs)
	}
}
