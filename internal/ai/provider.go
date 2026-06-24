package ai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultDeepSeekModel is the model name used when no model is explicitly configured.
const DefaultDeepSeekModel = "deepseek-v4-flash"

// DefaultGeminiModel is the model name used when no model is explicitly configured.
const DefaultGeminiModel = "gemini-2.5-flash"

// defaultClient is a shared HTTP client used when none is provided.
var defaultClient = &http.Client{Timeout: 60 * time.Second}

// Provider generates text using an AI model.
type Provider interface {
	GenerateText(ctx context.Context, prompt string) (string, error)
}

// ProviderConfig holds the parameters needed to construct a Provider.
type ProviderConfig struct {
	Name       string
	APIKey     string
	Model      string
	HTTPClient *http.Client // optional; defaults to a shared 60s client
}

// NewProvider constructs the appropriate Provider for the given config.
func NewProvider(cfg ProviderConfig) (Provider, error) {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = defaultClient
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Name))
	switch name {
	case "deepseek":
		model := cfg.Model
		if model == "" {
			model = DefaultDeepSeekModel
		}
		return &DeepSeekClient{
			APIKey:     cfg.APIKey,
			Model:      model,
			HTTPClient: httpClient,
		}, nil
	case "gemini":
		model := cfg.Model
		if model == "" {
			model = DefaultGeminiModel
		}
		return &GeminiClient{
			APIKey:     cfg.APIKey,
			Model:      model,
			HTTPClient: httpClient,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported AI provider %q", name)
	}
}
