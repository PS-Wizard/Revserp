package app

import (
	"context"
	"fmt"

	"github.com/ps-wizard/revserp/internal/ai"
)

func (a *App) generateAIText(ctx context.Context, prompt string) (string, string, error) {
	providerName := a.Config.AIProvider // already lowercased by config.Load()
	if providerName == "" {
		providerName = "deepseek"
	}

	var apiKey string
	switch providerName {
	case "deepseek":
		apiKey = a.Config.DeepSeekAPIKey
	case "gemini":
		apiKey = a.Config.GeminiAPIKey
	default:
		return "", "", fmt.Errorf("unsupported AI_PROVIDER %q", providerName)
	}

	model := "" // model is resolved by the factory default
	provider, err := ai.NewProvider(ai.ProviderConfig{
		Name:   providerName,
		APIKey: apiKey,
		Model:  "",
	})
	if err != nil {
		return "", "", err
	}

	content, err := provider.GenerateText(ctx, prompt)
	if err != nil {
		return "", "", err
	}

	// Determine which model was actually used by re-reading from config.
	// The factory applied defaults, but we return the configured model name.
	switch providerName {
	case "deepseek":
		model = a.Config.DeepSeekModel
		if model == "" {
			model = ai.DefaultDeepSeekModel
		}
	case "gemini":
		model = a.Config.GeminiModel
		if model == "" {
			model = ai.DefaultGeminiModel
		}
	}

	return content, model, nil
}
