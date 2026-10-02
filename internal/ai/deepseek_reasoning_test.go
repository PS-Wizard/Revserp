package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekReasoningReplayAcrossToolRounds(t *testing.T) {
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var assistantIndex int
		for _, message := range request.Messages {
			if message["role"] != "assistant" {
				continue
			}
			expected := ""
			if assistantIndex > 0 {
				expected = fmt.Sprintf("private-%d-complete", assistantIndex)
			}
			if got, exists := message["reasoning_content"]; !exists || got != expected {
				t.Errorf("round %d assistant %d reasoning replay missing or incorrect", round, assistantIndex)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assistantIndex++
		}
		if assistantIndex != round+1 {
			t.Errorf("round %d assistant count = %d", round, assistantIndex)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if round < 2 {
			writeSSE(t, w, fmt.Sprintf(`{"id":"x","choices":[{"delta":{"reasoning_content":"private-%d-"}}]}`, round+1))
			writeSSE(t, w, fmt.Sprintf(`{"id":"x","choices":[{"delta":{"reasoning_content":"complete","tool_calls":[{"index":0,"id":"call_%d","function":{"name":"read_issues","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, round+1))
		} else {
			writeSSE(t, w, `{"id":"x","choices":[{"delta":{"content":"Final answer"},"finish_reason":"stop"}]}`)
		}
		writeSSE(t, w, "[DONE]")
		round++
	}))
	defer server.Close()
	client := NewDeepSeekClient("test-key", "model", server.URL, nil)
	messages := []Message{{Role: RoleUser, Content: "Earlier question"}, {Role: RoleAssistant, Content: "Earlier answer"}, {Role: RoleUser, Content: "Check the issues"}}
	var answer strings.Builder
	for i := 0; i < 3; i++ {
		var reasoning strings.Builder
		var calls []ToolCall
		err := client.Stream(context.Background(), Request{
			Effort: "high", Messages: messages, Tools: []ToolDef{{Name: "read_issues", Schema: json.RawMessage(`{"type":"object"}`)}},
			OnReasoningDelta: func(delta string) { reasoning.WriteString(delta) },
		}, func(event Event) error {
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if strings.Contains(string(encoded), "private-") {
				t.Fatal("reasoning leaked into chat events")
			}
			answer.WriteString(event.Text)
			if event.ToolCall != nil {
				calls = append(calls, *event.ToolCall)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("round %d failed: %v", i, err)
		}
		if len(calls) > 0 {
			message := Message{Role: RoleAssistant, ToolCalls: calls, ReasoningContent: reasoning.String()}
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-") {
				t.Fatal("reasoning leaked through message serialization")
			}
			messages = append(messages, message, Message{Role: RoleTool, ToolCallID: calls[0].ID, Content: "No issues"})
		}
	}
	if answer.String() != "Final answer" || round != 3 {
		t.Fatalf("answer=%q rounds=%d", answer.String(), round)
	}
}

func TestDeepSeekRejectedRequestClassification(t *testing.T) {
	for _, status := range []int{400, 422, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"provider request rejected","type":"invalid_request_error","code":"invalid_request"}}`)
			}))
			defer server.Close()
			err := NewDeepSeekClient("test-key", "model", server.URL, nil).Stream(context.Background(), Request{}, func(Event) error { return nil })
			got := ClassifyError(err)
			want := ProviderError{Code: "provider_invalid_request"}
			if status == 429 {
				want = ProviderError{Code: "rate_limited", Temporary: true}
			}
			if status == 503 {
				want = ProviderError{Code: "provider_unavailable", Temporary: true}
			}
			if *got != want {
				t.Fatalf("classification=%+v want=%+v", got, want)
			}
		})
	}
}
