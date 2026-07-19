package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/app/aitools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestRunAgentTurn_UIActionsPersistBeforeEmission(t *testing.T) {
	for _, test := range []struct {
		name, event, payload string
		result               aitools.Result
	}{
		{"navigate", "navigate", `{"destination":"audit_seo"}`, aitools.Result{Content: "ok", Summary: "navigated", Destination: "audit_seo"}},
		{"project", "project_switched", `{"project_id":"project-1"}`, aitools.Result{Content: "ok", Summary: "switched", ProjectID: "project-1"}},
		{"audit export", "export", `{"kind":"audit","format":"pdf","project_id":"project-1","crawl_id":"crawl-1"}`, aitools.Result{Content: "ok", Summary: "export requested", ExportAction: &aitools.ExportAction{Kind: "audit", Format: "pdf", ProjectID: "project-1", CrawlID: "crawl-1"}}},
		{"csv export", "export", `{"kind":"crawl","format":"csv","project_id":"project-1","crawl_id":"crawl-1"}`, aitools.Result{Content: "ok", Summary: "export requested", ExportAction: &aitools.ExportAction{Kind: "crawl", Format: "csv", ProjectID: "project-1", CrawlID: "crawl-1"}}},
		{"xlsx export", "export", `{"kind":"crawl","format":"xlsx","project_id":"project-1","crawl_id":"crawl-1"}`, aitools.Result{Content: "ok", Summary: "export requested", ExportAction: &aitools.ExportAction{Kind: "crawl", Format: "xlsx", ProjectID: "project-1", CrawlID: "crawl-1"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{turns: [][]ai.StreamEvent{
				{{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call", Name: "tool", Args: `{}`}}, {Type: ai.EventDone}},
				{{Type: ai.EventDone}},
			}}
			registry := newFakeRegistry()
			registry.register("tool", func(context.Context, json.RawMessage, aitools.Scope) (aitools.Result, error) { return test.result, nil })
			sse, recorder := newTestSSE()
			if err := runAgentTurn(context.Background(), agentTurnParams{Client: client, Registry: registry, Queries: &fakePersister{}, ConversationID: fakeUUID(1), SSE: sse}); err != nil {
				t.Fatal(err)
			}
			events := parseSSEEvents(t, recorder.Body.String())
			if len(events) < 3 || events[1].Event != test.event || events[1].Data != test.payload || events[2].Event != "tool_result" {
				t.Fatalf("unexpected events: %+v", events)
			}
		})
	}
}

func TestRunAgentTurn_NoUIActionOnToolError(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{
		{{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call", Name: "tool", Args: `{}`}}, {Type: ai.EventDone}},
		{{Type: ai.EventDone}},
	}}
	registry := newFakeRegistry()
	registry.register("tool", func(context.Context, json.RawMessage, aitools.Scope) (aitools.Result, error) {
		return aitools.Result{ExportAction: &aitools.ExportAction{Kind: "audit", Format: "pdf", ProjectID: "project", CrawlID: "crawl"}}, context.Canceled
	})
	sse, recorder := newTestSSE()
	if err := runAgentTurn(context.Background(), agentTurnParams{Client: client, Registry: registry, Queries: &fakePersister{}, ConversationID: fakeUUID(1), SSE: sse}); err != nil {
		t.Fatal(err)
	}
	for _, event := range parseSSEEvents(t, recorder.Body.String()) {
		if event.Event == "navigate" || event.Event == "project_switched" || event.Event == "export" || event.Event == "crawl_started" {
			t.Fatalf("unexpected action: %+v", event)
		}
	}
}

type failToolPersister struct{ calls int }

func (p *failToolPersister) CreateAIMessage(_ context.Context, arg sqlc.CreateAIMessageParams) (sqlc.CreateAIMessageRow, error) {
	p.calls++
	if p.calls == 2 {
		return sqlc.CreateAIMessageRow{}, errors.New("persistence failed")
	}
	return sqlc.CreateAIMessageRow{ID: fakeUUID(byte(p.calls)), ConversationID: arg.ConversationID}, nil
}

func TestRunAgentTurn_NoUIActionOnToolPersistenceFailure(t *testing.T) {
	client := &scriptedClient{turns: [][]ai.StreamEvent{{
		{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "call", Name: "tool", Args: `{}`}}, {Type: ai.EventDone},
	}}}
	registry := newFakeRegistry()
	registry.register("tool", func(context.Context, json.RawMessage, aitools.Scope) (aitools.Result, error) {
		return aitools.Result{ExportAction: &aitools.ExportAction{Kind: "audit", Format: "pdf", ProjectID: "project", CrawlID: "crawl"}}, nil
	})
	sse, recorder := newTestSSE()
	if err := runAgentTurn(context.Background(), agentTurnParams{Client: client, Registry: registry, Queries: &failToolPersister{}, ConversationID: fakeUUID(1), SSE: sse}); err == nil {
		t.Fatal("expected persistence failure")
	}
	for _, event := range parseSSEEvents(t, recorder.Body.String()) {
		if event.Event == "export" {
			t.Fatalf("unexpected action: %+v", event)
		}
	}
}
