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

const defaultDeepSeekEndpoint = "https://api.deepseek.com/chat/completions"

// DeepSeekClient calls DeepSeek's OpenAI-compatible chat completions API.
type DeepSeekClient struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

type deepSeekChatRequest struct {
	Model       string            `json:"model"`
	Messages    []deepSeekMessage `json:"messages"`
	Temperature float64           `json:"temperature"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekChatResponse struct {
	Choices []struct {
		Message deepSeekMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateText sends one prompt to DeepSeek and returns the first text response.
func (client DeepSeekClient) GenerateText(ctx context.Context, prompt string) (string, error) {
	apiKey := strings.TrimSpace(client.APIKey)
	if apiKey == "" {
		return "", fmt.Errorf("missing DEEPSEEK_API_KEY")
	}

	model := strings.TrimSpace(client.Model)
	if model == "" {
		model = DefaultDeepSeekModel
	}

	payload := deepSeekChatRequest{
		Model: model,
		Messages: []deepSeekMessage{{
			Role:    "user",
			Content: prompt,
		}},
		Temperature: 0.2,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal deepseek request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultDeepSeekEndpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("build deepseek request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	httpClient := client.HTTPClient

	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("deepseek request failed: %w", err)
	}
	defer response.Body.Close()

	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("read deepseek response: %w", err)
	}
	if response.StatusCode >= 400 {
		// Cap embedded upstream body to 512 bytes to avoid leaking large error payloads.
		body := responseBytes
		if len(body) > 512 {
			body = body[:512]
		}
		return "", fmt.Errorf("deepseek error %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded deepSeekChatResponse
	if err := json.Unmarshal(responseBytes, &decoded); err != nil {
		return "", fmt.Errorf("decode deepseek response: %w", err)
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return "", fmt.Errorf("deepseek error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("empty deepseek response")
	}

	text := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty deepseek response")
	}

	return text, nil
}
