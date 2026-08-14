package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekStreamRequestMapping(t *testing.T) {
	for _, tc := range []struct {
		effort, thinking string
		hasEffort        bool
	}{
		{"none", "disabled", false},
		{"low", "enabled", true},
		{"high", "enabled", true},
		{"max", "enabled", true},
	} {
		t.Run(tc.effort, func(t *testing.T) {
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
				writeSSE(t, w, `{"id":"x","choices":[{"delta":{"content":"ok"}}]}`)
				writeSSE(t, w, "[DONE]")
			}))
			defer server.Close()

			err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{Effort: tc.effort, Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if request["max_tokens"] != float64(defaultChatMaxTokens) || request["max_completion_tokens"] != nil {
				t.Fatalf("token mapping: %#v", request)
			}
			streamOptions, ok := request["stream_options"].(map[string]any)
			if !ok || streamOptions["include_usage"] != true {
				t.Fatalf("include usage: %#v", request)
			}
			thinking, ok := request["thinking"].(map[string]any)
			if !ok || thinking["type"] != tc.thinking {
				t.Fatalf("thinking: %#v", request)
			}
			gotEffort, hasEffort := request["reasoning_effort"]
			if hasEffort != tc.hasEffort || (hasEffort && gotEffort != tc.effort) {
				t.Fatalf("reasoning effort: %#v", request)
			}
		})
	}
}

func TestDeepSeekStreamEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"reasoning_content":"hidden"}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"reasoning_content":"more hidden","content":"first"}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[{"delta":{"content":" second"}}]}`)
		writeSSE(t, w, `{"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":4}}}`)
		writeSSE(t, w, "[DONE]")
	}))
	defer server.Close()

	var events []Event
	err := NewDeepSeekClient("key", "model", server.URL, nil).Stream(context.Background(), Request{Effort: "high", Messages: []Message{{Role: "user", Content: "hi"}}}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || !events[0].Thinking || events[0].Text != "" || events[1].Text != "first" || events[2].Text != " second" || events[3].Usage == nil || *events[3].Usage != (Usage{Prompt: 1, Reasoning: 4, Completion: 2, Total: 3}) {
		t.Fatalf("events: %#v", events)
	}
}

func TestClassifyError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ProviderError
	}{
		{"preserves provider", &ProviderError{Code: "rate_limited", Temporary: true}, ProviderError{Code: "rate_limited", Temporary: true}},
		{"deadline", context.DeadlineExceeded, ProviderError{Code: "provider_timeout", Temporary: true}},
		{"network timeout", timeoutError{}, ProviderError{Code: "provider_timeout", Temporary: true}},
		{"unknown", errors.New("unknown"), ProviderError{Code: "provider_unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.err)
			if got == nil || *got != tc.want {
				t.Fatalf("ClassifyError(%v) = %#v, want %#v", tc.err, got, tc.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func writeSSE(t *testing.T, w io.Writer, data string) {
	t.Helper()
	if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
		t.Errorf("write SSE: %v", err)
	}
}
