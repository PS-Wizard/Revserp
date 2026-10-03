package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatStreamDiagnosticCallback(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		check   func(*testing.T, ChatStreamDiagnostic, error)
	}{
		{
			name: "http 503",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"sk-secret-abcdef","code":"internal"}}`))
			},
			check: func(t *testing.T, diag ChatStreamDiagnostic, err error) {
				if err == nil {
					t.Fatal("expected stream error")
				}
				if diag.Stage != "stream_error" || diag.Cause != "http_error" || diag.ErrorCode != "provider_unavailable" || diag.HTTPStatus != http.StatusServiceUnavailable {
					t.Errorf("503 diagnostic = %+v", diag)
				}
				if diag.HasUsage || diag.Chunks != 0 {
					t.Errorf("503 diagnostic counts = %+v", diag)
				}
			},
		},
		{
			name: "reasoning only length",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeSSE(t, w, `{"id":"x","choices":[{"delta":{"reasoning_content":"private-thinking"}}]}`)
				writeSSE(t, w, `{"id":"x","choices":[{"delta":{},"finish_reason":"length"}]}`)
				writeSSE(t, w, "[DONE]")
			},
			check: func(t *testing.T, diag ChatStreamDiagnostic, err error) {
				if diag.ErrorCode != "provider_unavailable" || diag.Cause != "reasoning_only" || diag.FinishReason != "length" {
					t.Errorf("reasoning-only diagnostic = %+v", diag)
				}
				if diag.ReasoningBytes == 0 || diag.TextBytes != 0 {
					t.Errorf("reasoning-only byte counts = %+v", diag)
				}
			},
		},
		{
			name: "success stop",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeSSE(t, w, `{"id":"x","choices":[{"delta":{"content":"hello"}}]}`)
				writeSSE(t, w, `{"id":"x","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
				writeSSE(t, w, "[DONE]")
			},
			check: func(t *testing.T, diag ChatStreamDiagnostic, err error) {
				if err != nil {
					t.Fatalf("stream error: %v", err)
				}
				if diag.ErrorCode != "" || diag.FinishReason != "stop" || diag.TextBytes != len("hello") {
					t.Errorf("success diagnostic = %+v", diag)
				}
				if !diag.HasUsage || diag.Usage.Total != 7 {
					t.Errorf("success usage = %+v", diag)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(testCase.handler)
			defer server.Close()
			client := NewDeepSeekClient("test-key", "model", server.URL, nil)
			var calls int
			var captured ChatStreamDiagnostic
			err := client.Stream(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
				OnStreamDiagnostic: func(diag ChatStreamDiagnostic) {
					calls++
					captured = diag
				},
			}, func(Event) error { return nil })
			if calls != 1 {
				t.Fatalf("OnStreamDiagnostic calls = %d, want 1", calls)
			}
			testCase.check(t, captured, err)
		})
	}
}
