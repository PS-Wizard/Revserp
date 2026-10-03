package aichatworker

import (
	"log"

	"github.com/ps-wizard/revserp/internal/ai"
)

type chatContextByteBreakdown struct {
	MessageContentBytes int
	ReplayedReasonBytes int
	ToolArgumentBytes   int
	ToolDefinitionBytes int
}

func measureChatRequestBytes(request ai.Request) chatContextByteBreakdown {
	replaysReasoning := ai.ChatReplaysReasoning(request)
	var out chatContextByteBreakdown
	for _, message := range request.Messages {
		out.MessageContentBytes += len(message.Content)
		if message.Role == ai.RoleAssistant {
			if replaysReasoning {
				out.ReplayedReasonBytes += len(message.ReasoningContent)
			}
			for _, call := range message.ToolCalls {
				out.ToolArgumentBytes += len(call.Args)
			}
		}
	}
	for _, tool := range request.Tools {
		out.ToolDefinitionBytes += len(tool.Name) + len(tool.Description) + len(tool.Schema)
	}
	return out
}

func logChatContextDiagnostic(kind, workerID, turnID, model string, round int, request ai.Request, inputTokens, budgetTokens int) {
	bytes := measureChatRequestBytes(request)
	log.Printf(
		"ai chat context diagnostic: kind=%q worker_id=%s turn_id=%s model=%q round=%d replays_reasoning=%t input_tokens_estimate=%d budget_tokens=%d message_content_bytes=%d replayed_reasoning_bytes=%d tool_argument_bytes=%d tool_definition_bytes=%d accounted_bytes=%d",
		kind,
		workerID,
		turnID,
		model,
		round,
		ai.ChatReplaysReasoning(request),
		inputTokens,
		budgetTokens,
		bytes.MessageContentBytes,
		bytes.ReplayedReasonBytes,
		bytes.ToolArgumentBytes,
		bytes.ToolDefinitionBytes,
		bytes.MessageContentBytes+bytes.ReplayedReasonBytes+bytes.ToolArgumentBytes+bytes.ToolDefinitionBytes,
	)
}
