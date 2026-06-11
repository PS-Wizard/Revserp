package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultGeminiEndpointBase = "https://generativelanguage.googleapis.com/v1beta"

// GeminiClient calls Gemini's generateContent HTTP API.
type GeminiClient struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

type geminiGenerateRequest struct {
	Contents         []geminiContent        `json:"contents"`
	GenerationConfig geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	Temperature float64 `json:"temperature"`
}

type geminiGenerateResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateText sends one prompt to Gemini and returns the first text response.
func (client GeminiClient) GenerateText(ctx context.Context, prompt string) (string, error) {
	apiKey := strings.TrimSpace(client.APIKey)
	if apiKey == "" {
		return "", fmt.Errorf("missing GEMINI_API_KEY")
	}

	model := strings.TrimSpace(client.Model)
	if model == "" {
		model = "gemini-2.5-flash"
	}

	payload := geminiGenerateRequest{
		Contents: []geminiContent{{
			Role:  "user",
			Parts: []geminiPart{{Text: prompt}},
		}},
		GenerationConfig: geminiGenerationConfig{Temperature: 0.2},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal gemini request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint(model, apiKey), bytes.NewReader(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("build gemini request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("gemini request failed: %w", err)
	}
	defer response.Body.Close()

	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("read gemini response: %w", err)
	}
	if response.StatusCode >= 400 {
		return "", fmt.Errorf("gemini error %d: %s", response.StatusCode, strings.TrimSpace(string(responseBytes)))
	}

	var decoded geminiGenerateResponse
	if err := json.Unmarshal(responseBytes, &decoded); err != nil {
		return "", fmt.Errorf("decode gemini response: %w", err)
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return "", fmt.Errorf("gemini error: %s", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 || len(decoded.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini response")
	}

	text := strings.TrimSpace(decoded.Candidates[0].Content.Parts[0].Text)
	if text == "" {
		return "", fmt.Errorf("empty gemini response")
	}

	return text, nil
}

// endpoint builds the direct Gemini generateContent URL.
func (client GeminiClient) endpoint(model string, apiKey string) string {
	return fmt.Sprintf("%s/models/%s:generateContent?key=%s", defaultGeminiEndpointBase, model, apiKey)
}
