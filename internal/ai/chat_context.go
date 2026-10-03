package ai

import (
	"math"
	"strings"
)

const (
	// ChatMaxOutputTokens reserves the same output allowance sent to the provider.
	ChatMaxOutputTokens = defaultChatMaxTokens

	chatMillionContextTokens = 1_000_000
	chatLegacyContextTokens  = 128_000
)

var chatMillionContextModels = map[string]bool{
	"deepseek-flash":               true,
	"deepseek-v4-flash":            true,
	"deepseek-v4-flash-vision-exp": true,
	"deepseek-v4-pro":              true,
}

// ChatContextCapacity returns the model's total context window in tokens,
// input plus ChatMaxOutputTokens. A blank model resolves to the existing default
// model; unknown or legacy models fall back to the conservative 128K legacy capacity.
func ChatContextCapacity(model string) int {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultDeepSeekModel
	}
	if chatMillionContextModels[model] {
		return chatMillionContextTokens
	}
	return chatLegacyContextTokens
}

// ChatReplaysReasoning reports whether assistant reasoning is serialized on the wire:
// only a request that offers tools and asks for reasoning replays it. DeepSeek requires
// the replay across tool rounds; without tools or with effort "none" the reasoning is
// never sent. Serialization and token accounting share this one predicate.
func ChatReplaysReasoning(request Request) bool {
	return len(request.Tools) > 0 && request.Effort != "none"
}

// EstimateChatInputTokens approximates the prompt tokens a chat request occupies:
// message text, replayed assistant reasoning (only when it reaches the wire),
// tool call IDs/names/arguments, tool results, tool definitions, per-message
// framing, and user images.
//
// It is a deliberately small stdlib-only heuristic, not DeepSeek's tokenizer:
// ASCII word characters count at ~4 characters per token, punctuation at ~2,
// and non-ASCII runes at ~1.5. Images use DeepSeek's documented 1024-token maximum.
// Prefer provider per-request usage when available; text estimates are not exact.
func EstimateChatInputTokens(request Request) int {
	const (
		chatMessageFramingTokens    = 4
		chatToolCallFramingTokens   = 8
		chatToolResultFramingTokens = 6
		chatToolDefFramingTokens    = 10
		// https://api-docs.deepseek.com/guides/vision/
		chatImageMaxTokens = 1024
	)
	replayReasoning := ChatReplaysReasoning(request)
	tokens := 0.0
	for _, message := range request.Messages {
		tokens += chatMessageFramingTokens
		tokens += float64(estimateTextTokens(message.Content))
		if message.Role == RoleAssistant && replayReasoning && message.ReasoningContent != "" {
			tokens += float64(estimateTextTokens(message.ReasoningContent))
		}
		if message.Role == RoleAssistant {
			for _, call := range message.ToolCalls {
				tokens += chatToolCallFramingTokens
				tokens += float64(estimateTextTokens(call.ID) + estimateTextTokens(call.Name) + estimateTextTokens(call.Args))
			}
		}
		if message.Role == RoleTool && message.ToolCallID != "" {
			tokens += chatToolResultFramingTokens + float64(estimateTextTokens(message.ToolCallID))
		}
		if message.Role != RoleSystem && message.Role != RoleAssistant && message.Role != RoleTool {
			tokens += float64(chatImageMaxTokens * len(message.Images))
		}
	}
	for _, tool := range request.Tools {
		tokens += chatToolDefFramingTokens
		tokens += float64(estimateTextTokens(tool.Name) + estimateTextTokens(tool.Description) + estimateTextTokens(string(tool.Schema)))
	}
	return int(math.Ceil(tokens))
}

func estimateTextTokens(text string) int {
	tokens := 0.0
	for _, r := range text {
		switch {
		case isASCIIWordRune(r):
			tokens += 0.25
		case r <= unicodeASCIIMax:
			tokens += 0.5
		default:
			tokens += 1.5
		}
	}
	return int(math.Ceil(tokens))
}

const unicodeASCIIMax = 0x7F

func isASCIIWordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		return true
	}
	return false
}
