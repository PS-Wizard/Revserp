package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/ps-wizard/revserp/internal/ai"
)

func (a *App) generateAIText(ctx context.Context, prompt string) (string, string, error) {
	provider := strings.ToLower(strings.TrimSpace(a.Config.AIProvider))
	if provider == "" {
		provider = "deepseek"
	}

	switch provider {
	case "deepseek":
		model := strings.TrimSpace(a.Config.DeepSeekModel)
		if model == "" {
			model = "deepseek-v4-flash"
		}
		client := ai.DeepSeekClient{APIKey: a.Config.DeepSeekAPIKey, Model: model}
		content, err := client.GenerateText(ctx, prompt)
		return content, model, err
	case "gemini":
		model := strings.TrimSpace(a.Config.GeminiModel)
		if model == "" {
			model = "gemini-2.5-flash"
		}
		client := ai.GeminiClient{APIKey: a.Config.GeminiAPIKey, Model: model}
		content, err := client.GenerateText(ctx, prompt)
		return content, model, err
	default:
		return "", "", fmt.Errorf("unsupported AI_PROVIDER %q", provider)
	}
}
