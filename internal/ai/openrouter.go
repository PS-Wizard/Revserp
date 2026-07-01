package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultOpenRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"

// OpenRouterClient calls OpenRouter's OpenAI-compatible chat completions API.
type OpenRouterClient struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

type openRouterChatRequest struct {
	Model       string               `json:"model"`
	Messages    []openRouterMessage  `json:"messages"`
	Temperature float64              `json:"temperature"`
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterChatResponse struct {
	Choices []struct {
		Message openRouterMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateText sends one prompt to OpenRouter and returns the first text response.
func (client OpenRouterClient) GenerateText(ctx context.Context, prompt string) (string, error) {
	apiKey := strings.TrimSpace(client.APIKey)
	if apiKey == "" {
		return "", fmt.Errorf("missing OPENROUTER_API_KEY")
	}

	model := strings.TrimSpace(client.Model)
	if model == "" {
		return "", fmt.Errorf("missing openrouter model")
	}

	payload := openRouterChatRequest{
		Model: model,
		Messages: []openRouterMessage{{
			Role:    "user",
			Content: prompt,
		}},
		Temperature: 0.3,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal openrouter request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultOpenRouterEndpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("build openrouter request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	httpClient := client.HTTPClient

	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("openrouter request failed: %w", err)
	}
	defer response.Body.Close()

	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("read openrouter response: %w", err)
	}
	if response.StatusCode >= 400 {
		return "", fmt.Errorf("openrouter error %d: %s", response.StatusCode, strings.TrimSpace(string(responseBytes)))
	}

	var decoded openRouterChatResponse
	if err := json.Unmarshal(responseBytes, &decoded); err != nil {
		return "", fmt.Errorf("decode openrouter response: %w", err)
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return "", fmt.Errorf("openrouter error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("empty openrouter response")
	}

	text := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty openrouter response")
	}

	return text, nil
}
