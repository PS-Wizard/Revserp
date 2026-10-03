package aichatworker

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/ai"
)

func TestLogChatProviderRequestDiagnosticLogsCountsNotPayload(t *testing.T) {
	secret := "sk-secret-abcdef123456 rawErrorBody"
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	logChatProviderRequestDiagnostic("worker-1", "turn-1", 2, "deepseek-chat", "high", 12*time.Millisecond, ai.ChatStreamDiagnostic{
		Stage:          "stream_error",
		Cause:          "http_error " + secret,
		ErrorCode:      "provider_unavailable",
		HTTPStatus:     503,
		FinishReason:   secret,
		Chunks:         4,
		TextBytes:      11,
		ReasoningBytes: 22,
		ToolCalls:      1,
		Usage:          ai.Usage{Prompt: 100, Reasoning: 20, Completion: 30, Total: 150},
		HasUsage:       true,
	})

	line := buf.String()
	if strings.Contains(line, secret) || strings.Contains(line, "sk-secret") || strings.Contains(line, "rawError") {
		t.Fatalf("provider payload leaked into worker log: %q", line)
	}
	for _, want := range []string{
		"ai chat provider request finished:",
		"worker_id=worker-1", "turn_id=turn-1", "round=2",
		`model="deepseek-chat"`, `effort="high"`, "elapsed_ms=12",
		`stage="stream_error"`, `cause="unknown"`, `error_code="provider_unavailable"`, `finish_reason="unknown"`,
		"http_status=503",
		"chunks=4", "text_bytes=11", "reasoning_bytes=22", "tool_calls=1",
		"prompt_tokens=100", "reasoning_tokens=20", "completion_tokens=30",
		"total_tokens=150", "usage_present=true",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("worker provider diagnostic log missing %q: %q", want, line)
		}
	}
}
