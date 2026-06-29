package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const defaultQuestionGenerationPrompt = `You are a search visibility analyst. Given a business profile and seed questions, generate 10 precise, natural-language questions that real users might type into an AI assistant or search engine to find a business like this one.

Rules:
- Each question must be a complete, standalone sentence ending with a question mark
- Questions should vary in intent: some informational, some commercial, some local
- Incorporate the brand name, category, and location naturally where appropriate
- Output exactly 10 questions, one per line, numbered 1-10
- No extra commentary, just the numbered questions`

func (w *Worker) handlePromptGeneration(ctx context.Context, job sqlc.AiWorkerJob) error {
	profile, err := w.queries.GetProjectBusinessProfileByProjectID(ctx, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no business profile for project %s", job.ProjectID.String())
		}
		return fmt.Errorf("load business profile: %w", err)
	}

	var seedPrompts []string
	if len(profile.SeedPrompts) > 0 {
		if err := json.Unmarshal(profile.SeedPrompts, &seedPrompts); err != nil {
			return fmt.Errorf("decode seed prompts: %w", err)
		}
	}
	if len(seedPrompts) == 0 {
		return fmt.Errorf("no seed prompts configured for project %s", job.ProjectID.String())
	}

	generationPrompt := defaultQuestionGenerationPrompt
	adminConfig, err := w.queries.GetAIPromptConfig(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load ai config: %w", err)
	}
	if err == nil && strings.TrimSpace(adminConfig.QuestionGenerationPrompt) != "" {
		generationPrompt = adminConfig.QuestionGenerationPrompt
	}

	model := w.cfg.DeepSeekModel
	if model == "" {
		model = "deepseek-v4-flash"
	}

	provider, err := ai.NewProvider(ai.ProviderConfig{
		Name:   "deepseek",
		APIKey: w.cfg.DeepSeekAPIKey,
		Model:  model,
	})
	if err != nil {
		return fmt.Errorf("create ai provider: %w", err)
	}

	prompt := buildGenerationPrompt(generationPrompt, profile, seedPrompts)
	raw, err := provider.GenerateText(ctx, prompt)
	if err != nil {
		return fmt.Errorf("generate questions: %w", err)
	}

	questions := parseGeneratedQuestions(raw)
	if len(questions) == 0 {
		return fmt.Errorf("ai returned no parseable questions")
	}

	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		return fmt.Errorf("marshal questions: %w", err)
	}

	if _, err := w.queries.UpsertProjectAIQuestions(ctx, sqlc.UpsertProjectAIQuestionsParams{
		ProjectID:       job.ProjectID,
		Questions:       questionsJSON,
		GenerationModel: model,
	}); err != nil {
		return fmt.Errorf("save questions: %w", err)
	}

	return nil
}

func buildGenerationPrompt(systemPrompt string, profile sqlc.GetProjectBusinessProfileByProjectIDRow, seedPrompts []string) string {
	var sb strings.Builder
	sb.WriteString(systemPrompt)
	sb.WriteString("\n\n---\nBusiness Profile:\n")
	sb.WriteString("Brand: ")
	sb.WriteString(profile.BrandName)
	if profile.WebsiteUrl != "" {
		sb.WriteString("\nWebsite: ")
		sb.WriteString(profile.WebsiteUrl)
	}
	if profile.PrimaryCategory.Valid && profile.PrimaryCategory.String != "" {
		sb.WriteString("\nCategory: ")
		sb.WriteString(profile.PrimaryCategory.String)
	}
	if profile.PrimaryLocation.Valid && profile.PrimaryLocation.String != "" {
		sb.WriteString("\nLocation: ")
		sb.WriteString(profile.PrimaryLocation.String)
	}
	if profile.BusinessDescription.Valid && profile.BusinessDescription.String != "" {
		sb.WriteString("\nDescription: ")
		sb.WriteString(profile.BusinessDescription.String)
	}
	sb.WriteString("\n\nSeed questions:\n")
	for i, p := range seedPrompts {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, p)
	}
	sb.WriteString("\nGenerate 10 questions:")
	return sb.String()
}

func parseGeneratedQuestions(raw string) []string {
	lines := strings.Split(raw, "\n")
	questions := make([]string, 0, 10)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip leading "1." or "1)" numbering (up to 2 digits + separator)
		for _, sep := range []string{". ", ") "} {
			if idx := strings.Index(line, sep); idx >= 0 && idx <= 3 {
				candidate := strings.TrimSpace(line[idx+len(sep):])
				if candidate != "" {
					line = candidate
					break
				}
			}
		}
		if len(line) > 10 {
			questions = append(questions, line)
		}
		if len(questions) == 10 {
			break
		}
	}
	return questions
}
