package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/app/aitools"
)

func TestRunAgentTurn_CrawlStartedEventAndMetadata(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{
		{{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call_1", Name: "start_crawl", Args: `{}`}}, {Type: ai.EventDone}},
		{{Type: ai.EventText, Delta: "queued"}, {Type: ai.EventDone}},
	}}
	registry := newFakeRegistry()
	registry.register("start_crawl", func(_ context.Context, _ json.RawMessage, scope aitools.Scope) (aitools.Result, error) {
		return aitools.Result{Content: `{"id":"crawl-1","status":"queued"}`, Summary: "crawl started", CrawlID: "crawl-1", CrawlProjectID: scope.ProjectID.String()}, nil
	})
	persister := &fakePersister{}
	sse, recorder := newTestSSE()
	if err := runAgentTurn(context.Background(), agentTurnParams{
		Client: client, Registry: registry, Queries: persister, ConversationID: fakeUUID(1),
		Scope: aitools.Scope{ProjectID: fakeUUID(3), CrawlID: fakeUUID(2)}, SystemPrompt: "system", UserContent: "start a crawl", SSE: sse,
	}); err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}
	events := parseSSEEvents(t, recorder.Body.String())
	if len(events) < 3 || events[1].Event != "crawl_started" || events[1].Data != `{"id":"crawl-1","project_id":"00000000-0000-0000-0000-000000000003"}` {
		t.Fatalf("expected crawl_started before tool_result, got %+v", events)
	}
	if events[2].Event != "tool_result" || events[2].Data != `{"id":"call_1","name":"start_crawl","summary":"crawl started"}` {
		t.Fatalf("unexpected tool result: %+v", events[2])
	}
}

func TestRunAgentTurn_NoCrawlStartedEventOnToolFailure(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{
		{{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call_1", Name: "start_crawl", Args: `{}`}}, {Type: ai.EventDone}},
		{{Type: ai.EventText, Delta: "failed"}, {Type: ai.EventDone}},
	}}
	registry := newFakeRegistry()
	registry.register("start_crawl", func(context.Context, json.RawMessage, aitools.Scope) (aitools.Result, error) {
		return aitools.Result{}, context.Canceled
	})
	persister := &fakePersister{}
	sse, recorder := newTestSSE()
	if err := runAgentTurn(context.Background(), agentTurnParams{
		Client: client, Registry: registry, Queries: persister, ConversationID: fakeUUID(1),
		Scope: aitools.Scope{CrawlID: fakeUUID(2)}, SystemPrompt: "system", UserContent: "start a crawl", SSE: sse,
	}); err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}
	for _, event := range parseSSEEvents(t, recorder.Body.String()) {
		if event.Event == "crawl_started" {
			t.Fatal("crawl_started emitted for failed tool")
		}
	}
}
