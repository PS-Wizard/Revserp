package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekStreamToolCallReassembly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_issues","arguments":"{\"ids\":"}}]}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"read_issues","arguments":""}},{"index":0,"function":{"arguments":"[1,2]}"}}]}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"ids\":[3]}"}}]}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		writeSSE(t, w, "[DONE]")
	}))
	defer server.Close()

	var calls []ToolCall
	var texts []string
	err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{
		Tools:    []ToolDef{{Name: "read_issues", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{{Role: RoleUser, Content: "show issues"}},
	}, func(event Event) error {
		if event.ToolCall != nil {
			calls = append(calls, *event.ToolCall)
		}
		if event.Text != "" {
			texts = append(texts, event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 0 {
		t.Fatalf("unexpected text events: %#v", texts)
	}
	want := []ToolCall{
		{ID: "call_1", Name: "read_issues", Args: `{"ids":[1,2]}`},
		{ID: "call_2", Name: "read_issues", Args: `{"ids":[3]}`},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %#v, want %#v", i, calls[i], want[i])
		}
	}
}

func TestDeepSeekStreamToolCallSafetyNet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No finish reason anywhere: the stream just ends.
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_issues","arguments":"{\"id\":"}}]}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"[7]}"}}]}}]}`)
		writeSSE(t, w, "[DONE]")
	}))
	defer server.Close()

	var calls []ToolCall
	err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{
		Tools:    []ToolDef{{Name: "read_issues", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{{Role: RoleUser, Content: "show issues"}},
	}, func(event Event) error {
		if event.ToolCall != nil {
			calls = append(calls, *event.ToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{{ID: "call_1", Name: "read_issues", Args: `{"id":[7]}`}}
	if len(calls) != len(want) || calls[0] != want[0] {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDeepSeekStreamRequestToolMapping(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		writeSSE(t, w, "[DONE]")
	}))
	defer server.Close()

	err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{
		Messages: []Message{
			{Role: RoleAssistant, Content: "let me look", ToolCalls: []ToolCall{{ID: "call_1", Name: "read_issues", Args: `{"ids":[1]}`}}},
			{Role: RoleTool, ToolCallID: "call_1", Name: "read_issues", Content: `[{"id":1}]`},
			{Role: RoleUser, Content: "thanks"},
		},
		Tools: []ToolDef{{Name: "read_issues", Description: "reads issues", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	tools, ok := request["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: %#v", request["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type: %#v", tool)
	}
	function := tool["function"].(map[string]any)
	if function["name"] != "read_issues" || function["description"] != "reads issues" {
		t.Fatalf("tool function: %#v", function)
	}
	parameters := function["parameters"].(map[string]any)
	if parameters["type"] != "object" {
		t.Fatalf("tool parameters: %#v", parameters)
	}

	rawMessages, ok := request["messages"].([]any)
	if !ok || len(rawMessages) != 3 {
		t.Fatalf("messages: %#v", request["messages"])
	}
	assistant := rawMessages[0].(map[string]any)
	if assistant["role"] != "assistant" || assistant["content"] != "let me look" {
		t.Fatalf("assistant message: %#v", assistant)
	}
	rawToolCalls := assistant["tool_calls"].([]any)
	if len(rawToolCalls) != 1 {
		t.Fatalf("assistant tool_calls: %#v", assistant["tool_calls"])
	}
	toolCall := rawToolCalls[0].(map[string]any)
	toolCallFunction := toolCall["function"].(map[string]any)
	if toolCall["id"] != "call_1" || toolCall["type"] != "function" ||
		toolCallFunction["name"] != "read_issues" || toolCallFunction["arguments"] != `{"ids":[1]}` {
		t.Fatalf("assistant tool call: %#v", toolCall)
	}
	toolMessage := rawMessages[1].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_1" || toolMessage["content"] != `[{"id":1}]` {
		t.Fatalf("tool message: %#v", toolMessage)
	}
	user := rawMessages[2].(map[string]any)
	if user["role"] != "user" || user["content"] != "thanks" {
		t.Fatalf("user message: %#v", user)
	}
}

func TestDeepSeekStreamInvalidToolSchema(t *testing.T) {
	// Schema validation fails before any network call; the URL is unreachable.
	err := NewDeepSeekClient("key", "model", "http://127.0.0.1:1", nil).Stream(context.Background(), Request{
		Tools: []ToolDef{{Name: "broken_tool", Schema: json.RawMessage(`{not json`)}},
	}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), `"broken_tool"`) {
		t.Fatalf("err = %v, want schema error naming the tool", err)
	}
}
