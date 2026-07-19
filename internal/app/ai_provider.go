package app

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/ps-wizard/revserp/internal/ai"
)

// generateAIText runs a one-shot (non-agentic) DeepSeek text generation for
// the legacy flat-prompt callers (ai_fix, crawl_commentary).
func (a *App) generateAIText(ctx context.Context, prompt string) (string, string, error) {
	model := strings.TrimSpace(a.Config.DeepSeekModel)
	if model == "" {
		model = "deepseek-v4-flash"
	}

	content, err := ai.NewDeepSeekClient(a.Config.DeepSeekAPIKey, model, a.Config.DeepSeekBaseURL, nil).GenerateText(ctx, prompt)
	if err != nil {
		log.Printf("ai provider request failed provider=%q model=%q error_type=%T", "deepseek", model, err)
		return "", "", errors.New("AI service unavailable")
	}
	return content, model, nil
}
