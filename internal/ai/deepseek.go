package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/shared"
)

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-v4-flash"
)

// DeepSeekClient calls DeepSeek's OpenAI-compatible chat completions API via
// the official openai-go SDK. It implements both Client (streaming,
// tool-calling turns) and Provider (one-shot text generation).
type DeepSeekClient struct {
	apiKey       string
	model        string
	client       openai.Client // used by GenerateText (one-shot, bounded timeout)
	streamClient openai.Client // used by StreamTurn (no overall timeout; see defaultStreamClient)
}

// NewDeepSeekClient builds a DeepSeekClient pointed at baseURL (defaulting to
// https://api.deepseek.com). httpClient may be nil, in which case GenerateText
// uses a shared bounded-timeout client and StreamTurn uses a separate shared
// client with no overall timeout (streaming responses must not be bounded by a
// single request-wide deadline). If httpClient is explicitly provided, it is
// used for both, preserving the caller's choice.
func NewDeepSeekClient(apiKey, model, baseURL string, httpClient *http.Client) *DeepSeekClient {
	if strings.TrimSpace(model) == "" {
		model = defaultDeepSeekModel
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDeepSeekBaseURL
	}

	oneShotHTTPClient := httpClient
	streamHTTPClient := httpClient
	if httpClient == nil {
		oneShotHTTPClient = defaultClient
		streamHTTPClient = defaultStreamClient
	}

	return &DeepSeekClient{
		apiKey: apiKey,
		model:  model,
		client: openai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey(apiKey),
			option.WithHTTPClient(oneShotHTTPClient),
		),
		streamClient: openai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey(apiKey),
			option.WithHTTPClient(streamHTTPClient),
		),
	}
}

// GenerateText sends one prompt to DeepSeek and returns the first text
// response. Kept for legacy one-shot callers.
func (c *DeepSeekClient) GenerateText(ctx context.Context, prompt string) (string, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return "", fmt.Errorf("missing DEEPSEEK_API_KEY")
	}

	response, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(c.model),
		Messages:    []openai.ChatCompletionMessageParamUnion{openai.UserMessage(prompt)},
		Temperature: param.NewOpt(0.2),
	})
	if err != nil {
		return "", fmt.Errorf("deepseek request failed: %w", err)
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("empty deepseek response")
	}

	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty deepseek response")
	}
	return text, nil
}

// StreamTurn runs one streaming turn, optionally with tools, and returns a
// channel of events. The channel is closed after a Done or Error event.
func (c *DeepSeekClient) StreamTurn(ctx context.Context, req TurnRequest) (<-chan StreamEvent, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, fmt.Errorf("missing DEEPSEEK_API_KEY")
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, toOpenAIMessage(message))
	}

	params := openai.ChatCompletionNewParams{
		Model:         openai.ChatModel(model),
		Messages:      messages,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if len(req.Tools) > 0 {
		tools := make([]openai.ChatCompletionToolParam, 0, len(req.Tools))
		for _, tool := range req.Tools {
			var schema shared.FunctionParameters
			if len(tool.Schema) > 0 {
				if err := json.Unmarshal(tool.Schema, &schema); err != nil {
					return nil, fmt.Errorf("invalid tool schema for %q: %w", tool.Name, err)
				}
			}
			function := shared.FunctionDefinitionParam{
				Name:       tool.Name,
				Parameters: schema,
			}
			if tool.Description != "" {
				function.Description = param.NewOpt(tool.Description)
			}
			tools = append(tools, openai.ChatCompletionToolParam{Function: function})
		}
		params.Tools = tools
	}

	stream := c.streamClient.Chat.Completions.NewStreaming(ctx, params)

	events := make(chan StreamEvent, 8)
	go pumpDeepSeekStream(ctx, stream, events)
	return events, nil
}

// toOpenAIMessage converts a Message to its openai-go wire representation.
// Note: Message intentionally has no reasoning field, so reasoning content
// is never round-tripped back to the API.
func toOpenAIMessage(message Message) openai.ChatCompletionMessageParamUnion {
	switch message.Role {
	case RoleSystem:
		return openai.SystemMessage(message.Content)
	case RoleTool:
		return openai.ToolMessage(message.Content, message.ToolCallID)
	case RoleAssistant:
		if len(message.ToolCalls) == 0 {
			return openai.AssistantMessage(message.Content)
		}
		assistant := openai.ChatCompletionAssistantMessageParam{
			ToolCalls: make([]openai.ChatCompletionMessageToolCallParam, 0, len(message.ToolCalls)),
		}
		if message.Content != "" {
			assistant.Content.OfString = param.NewOpt(message.Content)
		}
		for _, toolCall := range message.ToolCalls {
			assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallParam{
				ID: toolCall.ID,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      toolCall.Name,
					Arguments: toolCall.Args,
				},
			})
		}
		return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
	default:
		return openai.UserMessage(message.Content)
	}
}

// streamChunk is a provider-agnostic view of one streamed chat completion
// chunk, used to decouple the reassembly/emission logic from the SDK's wire
// types so it can be unit tested without a network dependency.
type streamChunk struct {
	Reasoning    string
	Text         string
	ToolCalls    []toolCallFragment
	FinishReason string
	Usage        *Usage
}

// deepSeekStream is the narrow stream contract used by the cancellation-safe
// pump. *ssestream.Stream[openai.ChatCompletionChunk] satisfies it.
type deepSeekStream interface {
	Next() bool
	Current() openai.ChatCompletionChunk
	Err() error
	Close() error
}

// pumpDeepSeekStream reads chunks off the DeepSeek stream, reassembles
// fragmented tool calls, and emits StreamEvents. It closes events when done.
func pumpDeepSeekStream(ctx context.Context, stream deepSeekStream, events chan<- StreamEvent) {
	defer close(events)
	defer stream.Close()

	accumulator := newToolCallAccumulator()
	var usage *Usage

	for stream.Next() {
		chunk := stream.Current()
		if !processStreamChunkContext(ctx, accumulator, toStreamChunk(chunk), events, &usage) {
			return
		}
	}
	if err := stream.Err(); err != nil {
		emitStreamEvent(ctx, events, StreamEvent{Type: EventError, Err: fmt.Errorf("deepseek stream: %w", err)})
		return
	}

	// Safety net in case the stream ended without an explicit finish reason.
	if !emitCompletedToolCallsContext(ctx, accumulator, events) {
		return
	}
	emitStreamEvent(ctx, events, StreamEvent{Type: EventDone, Usage: usage})
}

// toStreamChunk converts a raw openai-go stream chunk into a streamChunk.
// DeepSeek returns the model's chain-of-thought in a non-standard
// reasoning_content field on the delta, which openai-go surfaces as an
// unknown extra field; we extract it here and emit it as a reasoning delta.
func toStreamChunk(chunk openai.ChatCompletionChunk) streamChunk {
	var converted streamChunk
	if chunk.JSON.Usage.Valid() {
		converted.Usage = &Usage{
			PromptTokens:     int(chunk.Usage.PromptTokens),
			CompletionTokens: int(chunk.Usage.CompletionTokens),
			TotalTokens:      int(chunk.Usage.TotalTokens),
		}
	}
	if len(chunk.Choices) == 0 {
		return converted
	}
	choice := chunk.Choices[0]
	converted.Reasoning = extractReasoningContent(choice.Delta)
	converted.Text = choice.Delta.Content
	converted.FinishReason = choice.FinishReason
	for _, toolCall := range choice.Delta.ToolCalls {
		converted.ToolCalls = append(converted.ToolCalls, toolCallFragment{
			Index:     int(toolCall.Index),
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			ArgsDelta: toolCall.Function.Arguments,
		})
	}
	return converted
}

// extractReasoningContent pulls DeepSeek's non-standard reasoning_content
// delta out of the SDK's extra-fields bag. It degrades to an empty string if
// the field is absent or not a JSON string.
func extractReasoningContent(delta openai.ChatCompletionChunkChoiceDelta) string {
	// openai-go exposes unknown provider fields via JSON.ExtraFields. Extra
	// fields report Valid()==false (they are not part of the typed schema), so
	// we gate on presence + a non-empty raw value rather than Valid().
	field, ok := delta.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return ""
	}
	raw := field.Raw()
	if raw == "" {
		return ""
	}
	var reasoning string
	if err := json.Unmarshal([]byte(raw), &reasoning); err != nil {
		return ""
	}
	return reasoning
}

// processStreamChunk folds one streamChunk into the accumulator and emits
// the corresponding StreamEvents. Tool calls are emitted once complete, at
// the chunk carrying a non-empty finish reason. usage is updated in place
// whenever the chunk carries it.
func processStreamChunk(accumulator *toolCallAccumulator, chunk streamChunk, events chan<- StreamEvent, usage **Usage) {
	processStreamChunkContext(context.Background(), accumulator, chunk, events, usage)
}

func processStreamChunkContext(ctx context.Context, accumulator *toolCallAccumulator, chunk streamChunk, events chan<- StreamEvent, usage **Usage) bool {
	if chunk.Usage != nil {
		*usage = chunk.Usage
	}
	if chunk.Reasoning != "" && !emitStreamEvent(ctx, events, StreamEvent{Type: EventReasoning, Delta: chunk.Reasoning}) {
		return false
	}
	if chunk.Text != "" && !emitStreamEvent(ctx, events, StreamEvent{Type: EventText, Delta: chunk.Text}) {
		return false
	}
	for _, fragment := range chunk.ToolCalls {
		accumulator.add(fragment)
	}
	if chunk.FinishReason != "" && !accumulator.empty() {
		return emitCompletedToolCallsContext(ctx, accumulator, events)
	}
	return true
}

func emitCompletedToolCallsContext(ctx context.Context, accumulator *toolCallAccumulator, events chan<- StreamEvent) bool {
	for _, toolCall := range accumulator.drain() {
		toolCall := toolCall
		if !emitStreamEvent(ctx, events, StreamEvent{Type: EventToolCall, ToolCall: &toolCall}) {
			return false
		}
	}
	return true
}

// emitStreamEvent never lets a disconnected consumer strand the pump.
func emitStreamEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

// compile-time assertion that the concrete SDK stream satisfies deepSeekStream.
var _ deepSeekStream = (*ssestream.Stream[openai.ChatCompletionChunk])(nil)
