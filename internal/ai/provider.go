package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Role identifies who authored a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a single invocation of a tool requested by the model.
type ToolCall struct {
	ID   string
	Name string
	Args string // raw JSON arguments
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for the tool's parameters
}

// Provider generates text using an AI model.
type Provider interface {
	GenerateText(ctx context.Context, prompt string) (string, error)
}

// ProviderConfig holds the parameters needed to construct a Provider.
type ProviderConfig struct {
	Name       string
	APIKey     string
	Model      string
	BaseURL    string       // optional; DeepSeek defaults to https://api.deepseek.com
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
		return NewDeepSeekClient(cfg.APIKey, cfg.Model, cfg.BaseURL, httpClient), nil
	case "openrouter":
		return &OpenRouterClient{
			APIKey:     cfg.APIKey,
			Model:      cfg.Model,
			HTTPClient: httpClient,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported AI provider %q", name)
	}
}
