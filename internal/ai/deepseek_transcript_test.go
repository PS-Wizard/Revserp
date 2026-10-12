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

func deepseekTranscriptHistory() []Message {
	return []Message{
		{Role: RoleUser, Content: "first question", Images: []Image{{MediaType: "image/jpeg", Data: "abc"}}},
		{Role: RoleAssistant, Content: "checking skills", ReasoningContent: "r1-private", ToolCalls: []ToolCall{{ID: "call_1", Name: "read_skill", Args: `{"skill":"seo/local-seo"}`}}},
		{Role: RoleTool, ToolCallID: "call_1", Content: "skill body"},
		{Role: RoleAssistant, Content: "checking page", ReasoningContent: "r2-private", ToolCalls: []ToolCall{{ID: "call_2", Name: "read_page", Args: `{"url":"https://example.com"}`}}},
		{Role: RoleTool, ToolCallID: "call_2", Content: "page text"},
		{Role: RoleAssistant, Content: "earlier answer", ReasoningContent: "final-private"},
		{Role: RoleUser, Content: "follow up"},
	}
}

func deepseekCaptureServer(t *testing.T, captured *map[string]any, chunks ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		*captured = request
		for _, chunk := range chunks {
			writeSSE(t, w, chunk)
		}
		writeSSE(t, w, "[DONE]")
	}))
}

func deepseekWireMessages(t *testing.T, request map[string]any) []map[string]any {
	t.Helper()
	raw, ok := request["messages"].([]any)
	if !ok || len(raw) != 7 {
		t.Fatalf("messages = %#v, want full 7-message transcript", request["messages"])
	}
	messages := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		message, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("message = %#v, want object", item)
		}
		messages = append(messages, message)
	}
	return messages
}

func TestDeepSeekTranscriptReplayKeepsFullHistory(t *testing.T) {
	var request map[string]any
	server := deepseekCaptureServer(t, &request,
		`{"id":"x","choices":[{"delta":{"reasoning_content":"new-private"}}]}`,
		`{"id":"x","choices":[{"delta":{"content":"new answer"},"finish_reason":"stop"}]}`,
	)
	defer server.Close()

	var reasoning, answer strings.Builder
	tools := []ToolDef{
		{Name: "read_skill", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_page", Schema: json.RawMessage(`{"type":"object"}`)},
	}
	err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{
		Effort: "high", Messages: deepseekTranscriptHistory(), Tools: tools,
		OnReasoningDelta: func(delta string) { reasoning.WriteString(delta) },
	}, func(event Event) error {
		if strings.Contains(event.Text, "private") {
			t.Error("reasoning leaked into chat text")
		}
		answer.WriteString(event.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reasoning.String() != "new-private" {
		t.Fatalf("reasoning callback = %q, want new-private only", reasoning.String())
	}
	if answer.String() != "new answer" {
		t.Fatalf("answer = %q, want new answer", answer.String())
	}

	messages := deepseekWireMessages(t, request)
	for i, role := range []string{"user", "assistant", "tool", "assistant", "tool", "assistant", "user"} {
		if messages[i]["role"] != role {
			t.Fatalf("messages[%d] role = %#v, want %q", i, messages[i]["role"], role)
		}
	}

	parts, ok := messages[0]["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("first user content parts = %#v, want text plus image", messages[0]["content"])
	}
	imagePart, _ := parts[1].(map[string]any)
	imageURL, _ := imagePart["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/jpeg;base64,abc" {
		t.Fatalf("cross-turn image url = %#v", imagePart)
	}

	rounds := []struct{ content, reasoning, callID, callName, callArgs string }{
		{"checking skills", "r1-private", "call_1", "read_skill", `{"skill":"seo/local-seo"}`},
		{"checking page", "r2-private", "call_2", "read_page", `{"url":"https://example.com"}`},
	}
	for i, want := range rounds {
		assistant := messages[1+i*2]
		if assistant["content"] != want.content || assistant["reasoning_content"] != want.reasoning {
			t.Fatalf("assistant %d = %#v, want content+reasoning verbatim", i, assistant)
		}
		calls, ok := assistant["tool_calls"].([]any)
		if !ok || len(calls) != 1 {
			t.Fatalf("assistant %d tool_calls = %#v, want one call", i, assistant["tool_calls"])
		}
		call, _ := calls[0].(map[string]any)
		function, _ := call["function"].(map[string]any)
		if call["id"] != want.callID || function["name"] != want.callName || function["arguments"] != want.callArgs {
			t.Fatalf("assistant %d tool call = %#v", i, call)
		}
		tool := messages[2+i*2]
		if tool["tool_call_id"] != want.callID {
			t.Fatalf("tool result %d = %#v, want call %q", i, tool, want.callID)
		}
	}
	if messages[5]["content"] != "earlier answer" || messages[5]["reasoning_content"] != "final-private" {
		t.Fatalf("final assistant = %#v, want earlier content+reasoning verbatim", messages[5])
	}
	if messages[6]["content"] != "follow up" {
		t.Fatalf("new user message = %#v", messages[6])
	}
}

func TestDeepSeekTranscriptOmitsReasoningWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effort string
		tools  []ToolDef
	}{
		{"effort none", "none", []ToolDef{{Name: "read_skill", Schema: json.RawMessage(`{"type":"object"}`)}}},
		{"no tools", "high", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request map[string]any
			server := deepseekCaptureServer(t, &request,
				`{"id":"x","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			)
			defer server.Close()

			err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{
				Effort: tc.effort, Messages: deepseekTranscriptHistory(), Tools: tc.tools,
			}, func(Event) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			messages := deepseekWireMessages(t, request)
			for _, message := range messages {
				if message["role"] != "assistant" {
					continue
				}
				if _, ok := message["reasoning_content"]; ok {
					t.Fatalf("reasoning replayed without tools+effort: %#v", message)
				}
			}
			if messages[1]["content"] != "checking skills" {
				t.Fatalf("assistant content = %#v, want public content kept", messages[1])
			}
			calls, ok := messages[1]["tool_calls"].([]any)
			if !ok || len(calls) != 1 {
				t.Fatalf("assistant tool_calls = %#v, want tool evidence kept", messages[1]["tool_calls"])
			}
			if messages[2]["content"] != "skill body" || messages[2]["tool_call_id"] != "call_1" {
				t.Fatalf("tool result = %#v, want result kept", messages[2])
			}
		})
	}
}
