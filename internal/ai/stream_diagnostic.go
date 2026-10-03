package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"

	openai "github.com/openai/openai-go"
)

// Stage labels for ChatStreamDiagnostic. Fixed literals, never provider strings.
const (
	diagStageMissingKey     = "missing_key"
	diagStageDecodeDelta    = "decode_delta"
	diagStageDecodeUsage    = "decode_usage"
	diagStageDecodeSchema   = "decode_schema"
	diagStageStreamError    = "stream_error"
	diagStageEmitError      = "emit_error"
	diagStageEmptyResponse  = "empty_response"
	diagStageStreamComplete = "stream_complete"
)

// Cause labels for ChatStreamDiagnostic. Fixed literals, never provider strings.
const (
	diagCauseHTTPError        = "http_error"
	diagCauseDeadlineExceeded = "deadline_exceeded"
	diagCauseCancelled        = "cancelled"
	diagCauseNetworkTimeout   = "network_timeout"
	diagCauseNetworkError     = "network_error"
	diagCauseUnexpectedEOF    = "unexpected_eof"
	diagCauseInvalidJSON      = "invalid_json"
	diagCauseReasoningOnly    = "reasoning_only"
	diagCauseEmptyResponse    = "empty_response"
	diagCauseUnknownError     = "unknown_error"
)

const diagFinishUnknown = "unknown"

// streamDiagnostic accumulates counts only. It never retains payloads, and it
// publishes to the private per-request callback exactly once.
type streamDiagnostic struct {
	report func(ChatStreamDiagnostic)

	diag     ChatStreamDiagnostic
	stage    string
	cause    string
	reported bool
}

// newStreamDiagnostic builds a collector for report, which may be nil.
func newStreamDiagnostic(report func(ChatStreamDiagnostic)) *streamDiagnostic {
	return &streamDiagnostic{report: report}
}

func (d *streamDiagnostic) countChunk()              { d.diag.Chunks++ }
func (d *streamDiagnostic) countText(bytes int)      { d.diag.TextBytes += bytes }
func (d *streamDiagnostic) countReasoning(bytes int) { d.diag.ReasoningBytes += bytes }
func (d *streamDiagnostic) countEmittedToolCall()    { d.diag.ToolCalls++ }
func (d *streamDiagnostic) sawReasoning() bool       { return d.diag.ReasoningBytes > 0 }

// setUsage keeps the latest usage chunk of this request, never a cumulative sum.
func (d *streamDiagnostic) setUsage(usage Usage) {
	d.diag.Usage = usage
	d.diag.HasUsage = true
}

// setFinishReason keeps only allowlisted provider values.
func (d *streamDiagnostic) setFinishReason(reason string) {
	if reason != "" {
		d.diag.FinishReason = sanitizeDiagFinishReason(reason)
	}
}

// stop labels the terminal outcome. It records only ClassifyError's stable code
// and the numeric provider status; no error text ever reaches the diagnostic.
func (d *streamDiagnostic) stop(stage, cause string, err error) {
	d.stage = stage
	d.cause = cause
	if err != nil {
		d.diag.ErrorCode = ClassifyError(err).Code
		d.diag.HTTPStatus = diagHTTPStatus(err)
	}
}

// stopErr labels the outcome with a cause derived from err.
func (d *streamDiagnostic) stopErr(stage string, err error) {
	d.stop(stage, classifyDiagCause(err), err)
}

// finish publishes the single diagnostic for this request.
func (d *streamDiagnostic) finish() {
	if d.report == nil || d.reported {
		return
	}
	d.reported = true
	d.diag.Stage = d.stage
	d.diag.Cause = d.cause
	d.report(d.diag)
}

// sanitizeDiagFinishReason maps unknown values to "unknown" and keeps absence empty.
func sanitizeDiagFinishReason(reason string) string {
	switch reason {
	case "":
		return ""
	case "stop", "length", "tool_calls", "content_filter", "function_call":
		return reason
	default:
		return diagFinishUnknown
	}
}

// classifyDiagCause derives a fixed cause label from a transport failure.
func classifyDiagCause(err error) string {
	if err == nil {
		return diagCauseUnknownError
	}
	var providerError *openai.Error
	if errors.As(err, &providerError) && providerError.StatusCode != 0 {
		return diagCauseHTTPError
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return diagCauseDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return diagCauseCancelled
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return diagCauseNetworkTimeout
		}
		return diagCauseNetworkError
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return diagCauseUnexpectedEOF
	}
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntaxError) || errors.As(err, &typeError) {
		return diagCauseInvalidJSON
	}
	return diagCauseUnknownError
}

// diagHTTPStatus extracts only the numeric provider status, if present.
func diagHTTPStatus(err error) int {
	var providerError *openai.Error
	if errors.As(err, &providerError) {
		return providerError.StatusCode
	}
	return 0
}
