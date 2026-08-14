package ai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
)

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-v4-flash"
)

// DeepSeekClient calls DeepSeek's OpenAI-compatible chat completions API.
type DeepSeekClient struct {
	apiKey string
	model  string
	client openai.Client
}

// NewDeepSeekClient builds a DeepSeekClient pointed at baseURL.
func NewDeepSeekClient(apiKey, model, baseURL string, httpClient *http.Client) *DeepSeekClient {
	if strings.TrimSpace(model) == "" {
		model = defaultDeepSeekModel
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDeepSeekBaseURL
	}
	if httpClient == nil {
		httpClient = defaultClient
	}
	return &DeepSeekClient{
		apiKey: apiKey,
		model:  model,
		client: openai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey(apiKey),
			option.WithHTTPClient(httpClient),
		),
	}
}

// GenerateText sends one prompt to DeepSeek and returns the first text response.
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
