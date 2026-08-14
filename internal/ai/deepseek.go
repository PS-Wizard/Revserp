package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
)

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-v4-flash"
	defaultChatMaxTokens   = 2048
)

// Message is one text-only model input message.
type Message struct {
	Role    string
	Content string
}

// Request is one text-only streaming chat request.
type Request struct {
	Model    string
	Effort   string
	Messages []Message
}

// Usage contains provider token counts when the provider supplies them.
type Usage struct {
	Prompt     int
	Reasoning  int
	Completion int
	Total      int
}

// Event is one provider stream event. Reasoning text is never included.
type Event struct {
	Thinking bool
	Text     string
	Usage    *Usage
}

// ProviderError is a stable, payload-free provider failure classification.
type ProviderError struct {
	Code      string
	Temporary bool
}

func (err *ProviderError) Error() string { return err.Code }

// ClassifyError maps transport failures to stable worker codes.
func ClassifyError(err error) *ProviderError {
	var classified *ProviderError
	if errors.As(err, &classified) {
		return classified
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ProviderError{Code: "provider_timeout", Temporary: true}
	}
	var providerError *openai.Error
	if errors.As(err, &providerError) {
		if providerError.Code == "context_length_exceeded" || providerError.Type == "context_length_exceeded" {
			return &ProviderError{Code: "context_too_large"}
		}
		switch {
		case providerError.StatusCode == http.StatusRequestTimeout:
			return &ProviderError{Code: "provider_timeout", Temporary: true}
		case providerError.StatusCode == http.StatusTooManyRequests:
			return &ProviderError{Code: "rate_limited", Temporary: true}
		case providerError.StatusCode >= 500:
			return &ProviderError{Code: "provider_unavailable", Temporary: true}
		default:
			return &ProviderError{Code: "provider_unavailable"}
		}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return &ProviderError{Code: "provider_timeout", Temporary: true}
		}
		return &ProviderError{Code: "provider_unavailable", Temporary: true}
	}
	return &ProviderError{Code: "provider_unavailable"}
}

// Streamer is the narrow worker/provider seam.
type Streamer interface {
	Stream(context.Context, Request, func(Event) error) error
}

// DeepSeekClient calls DeepSeek's OpenAI-compatible chat completion endpoint.
type DeepSeekClient struct {
	apiKey       string
	model        string
	client       openai.Client
	streamClient openai.Client
}

// NewDeepSeekClient builds a DeepSeek client pointed at baseURL.
func NewDeepSeekClient(apiKey, model, baseURL string, httpClient *http.Client) *DeepSeekClient {
	if strings.TrimSpace(model) == "" {
		model = defaultDeepSeekModel
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDeepSeekBaseURL
	}
	requestClient := httpClient
	streamHTTPClient := httpClient
	if requestClient == nil {
		requestClient = defaultClient
		// The worker context owns the stream timeout. An http.Client timeout would
		// end healthy streams before AI_TURN_TIMEOUT.
		streamHTTPClient = &http.Client{}
	}
	newClient := func(client *http.Client) openai.Client {
		return openai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey(apiKey),
			option.WithHTTPClient(client),
		)
	}
	return &DeepSeekClient{
		apiKey:       apiKey,
		model:        model,
		client:       newClient(requestClient),
		streamClient: newClient(streamHTTPClient),
	}
}

// GenerateText preserves one-shot generation for non-chat AI callers.
func (client *DeepSeekClient) GenerateText(ctx context.Context, prompt string) (string, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return "", fmt.Errorf("missing DEEPSEEK_API_KEY")
	}
	response, err := client.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(client.model),
		Messages:    []openai.ChatCompletionMessageParamUnion{openai.UserMessage(prompt)},
		Temperature: param.NewOpt(0.2),
	})
	if err != nil {
		return "", fmt.Errorf("deepseek request failed: %w", err)
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("empty deepseek response")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}

// Stream sends a text-only chat and discards all raw reasoning content.
func (client *DeepSeekClient) Stream(ctx context.Context, request Request, emit func(Event) error) error {
	if strings.TrimSpace(client.apiKey) == "" {
		return &ProviderError{Code: "provider_unavailable"}
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = client.model
	}
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(request.Messages))
	for _, message := range request.Messages {
		switch message.Role {
		case "system":
			messages = append(messages, openai.SystemMessage(message.Content))
		case "assistant":
			messages = append(messages, openai.AssistantMessage(message.Content))
		default:
			messages = append(messages, openai.UserMessage(message.Content))
		}
	}

	params := openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(model),
		Messages:  messages,
		MaxTokens: param.NewOpt(int64(defaultChatMaxTokens)),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}
	thinkingType := "disabled"
	if request.Effort != "none" {
		thinkingType = "enabled"
		params.ReasoningEffort = openai.ReasoningEffort(request.Effort)
	}
	params.SetExtraFields(map[string]any{
		"thinking": map[string]string{"type": thinkingType},
	})

	stream := client.streamClient.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()
	reasoningStarted := false
	answerStarted := false
	for stream.Next() {
		chunk := stream.Current()
		for _, choice := range chunk.Choices {
			var providerDelta struct {
				ReasoningContent string `json:"reasoning_content"`
			}
			if err := json.Unmarshal([]byte(choice.Delta.RawJSON()), &providerDelta); err != nil {
				return &ProviderError{Code: "provider_unavailable"}
			}
			if providerDelta.ReasoningContent != "" && !reasoningStarted {
				reasoningStarted = true
				if err := emit(Event{Thinking: true}); err != nil {
					return err
				}
			}
			if choice.Delta.Content != "" {
				answerStarted = true
				if err := emit(Event{Text: choice.Delta.Content}); err != nil {
					return err
				}
			}
		}
		if chunk.JSON.Usage.Valid() {
			usage := Usage{
				Prompt:     int(chunk.Usage.PromptTokens),
				Completion: int(chunk.Usage.CompletionTokens),
				Total:      int(chunk.Usage.TotalTokens),
			}
			var providerUsage struct {
				Usage struct {
					ReasoningTokens         int `json:"reasoning_tokens"`
					CompletionTokensDetails struct {
						ReasoningTokens int `json:"reasoning_tokens"`
					} `json:"completion_tokens_details"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(chunk.RawJSON()), &providerUsage); err != nil {
				return &ProviderError{Code: "provider_unavailable"}
			}
			usage.Reasoning = providerUsage.Usage.CompletionTokensDetails.ReasoningTokens
			if usage.Reasoning == 0 {
				usage.Reasoning = providerUsage.Usage.ReasoningTokens
			}
			if err := emit(Event{Usage: &usage}); err != nil {
				return err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return ClassifyError(err)
	}
	if !answerStarted {
		return &ProviderError{Code: "provider_unavailable", Temporary: true}
	}
	return nil
}
