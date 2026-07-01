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

const DefaultQuestionGenerationPrompt = `You are a search visibility analyst. Your job is to write questions that test whether a business surfaces on its own when potential customers ask an AI assistant for recommendations — WITHOUT ever naming that business.

You are given a business profile (brand, category, location, audience, description) and seed questions. Use them ONLY to understand the category and target customer. Then generate 10 natural-language questions that a potential customer who has never heard of this specific brand would ask an AI assistant while shopping for a product, service, or tool like this one.

Critical rules:
- NEVER mention the brand name, domain, or any unique product name. The entire point is to see whether the brand appears unprompted. A question containing the brand is invalid.
- Every question must be answerable as a LIST of businesses, products, tools, or services — i.e. a "recommend me options" question. Never a yes/no, and never a lookup about one specific company.
- Write from the customer's point of view: their need, use case, industry, budget, team size, or location.
- Vary the angle across the 10: best-in-category, alternatives to a well-known competitor, best for a specific use case, best for a specific audience or budget, best in a specific location.
- Do NOT ask about a single company's pricing, features, or "what is X" — those cannot be answered as a discovery list.
- Each question must be a complete, standalone sentence ending with a question mark.
- Output exactly 10 questions, one per line, numbered 1-10. No extra commentary.

Good examples (category-based, brand never named):
1. What are the best marketing analytics platforms for a small SaaS team?
2. Which SEO tools are solid alternatives to Semrush for a lean B2B startup?
3. What affordable lead-tracking tools work well for an early-stage US SaaS company?

Bad examples (never do this):
- "What is Northstar Reach and how does it help my team?" (names the brand; not discovery)
- "What are the pricing plans for Northstar Reach?" (single-company lookup; not a list)`

func (w *Worker) handlePromptGeneration(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
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

	generationPrompt := DefaultQuestionGenerationPrompt
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

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
			if idx := strings.Index(line, sep); idx >= 0 && idx <= 3 && isDigits(line[:idx]) {
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
