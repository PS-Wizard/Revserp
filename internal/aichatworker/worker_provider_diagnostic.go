package aichatworker

import (
	"log"
	"strings"
	"time"

	"github.com/ps-wizard/revserp/internal/ai"
)

// chatProviderDiagnosticEnums is the closed set of labels the provider
// classification may emit. Anything else is logged as "unknown" so a
// provider payload can never be mistaken for an enum.
var chatProviderDiagnosticEnums = map[string]bool{
	"none": true, "unknown": true,
	"missing_key": true, "decode_delta": true, "decode_usage": true, "decode_schema": true,
	"stream_error": true, "emit_error": true, "empty_response": true, "stream_complete": true,
	"provider_unavailable": true, "provider_timeout": true,
	"provider_invalid_request": true, "rate_limited": true, "context_too_large": true,
	"http_error": true, "deadline_exceeded": true, "cancelled": true,
	"network_timeout": true, "network_error": true, "unexpected_eof": true,
	"invalid_json": true, "reasoning_only": true, "unknown_error": true,
	"stop": true, "length": true, "tool_calls": true, "content_filter": true, "function_call": true,
}

// diagnosticLabel keeps only fixed enum labels, so no provider payload, URL,
// header, or raw error text can reach the worker log.
func diagnosticLabel(value string) string {
	if value == "" {
		return "none"
	}
	if chatProviderDiagnosticEnums[value] {
		return value
	}
	return "unknown"
}

// diagnosticName keeps only a short identifier-shaped config value (model or
// effort) for the provider request log.
func diagnosticName(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

// logChatProviderRequestDiagnostic logs counts and fixed labels for one
// finished provider request. It never logs content, reasoning, tool arguments,
// bodies, headers, URLs, credentials, or raw error strings.
func logChatProviderRequestDiagnostic(workerID, turnID string, round int, model, effort string, elapsed time.Duration, diag ai.ChatStreamDiagnostic) {
	log.Printf(
		"ai chat provider request finished: worker_id=%s turn_id=%s round=%d model=%q effort=%q elapsed_ms=%d stage=%q cause=%q error_code=%q http_status=%d finish_reason=%q chunks=%d text_bytes=%d reasoning_bytes=%d tool_calls=%d prompt_tokens=%d reasoning_tokens=%d completion_tokens=%d total_tokens=%d usage_present=%t",
		workerID,
		turnID,
		round,
		diagnosticName(model),
		diagnosticName(effort),
		elapsed.Milliseconds(),
		diagnosticLabel(diag.Stage),
		diagnosticLabel(diag.Cause),
		diagnosticLabel(diag.ErrorCode),
		diag.HTTPStatus,
		diagnosticLabel(diag.FinishReason),
		diag.Chunks,
		diag.TextBytes,
		diag.ReasoningBytes,
		diag.ToolCalls,
		diag.Usage.Prompt,
		diag.Usage.Reasoning,
		diag.Usage.Completion,
		diag.Usage.Total,
		diag.HasUsage,
	)
}
