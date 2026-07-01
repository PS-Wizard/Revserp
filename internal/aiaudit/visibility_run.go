package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

var numberedItemRe = regexp.MustCompile(`(?m)^\s*\d+[.)]\s+(.+)`)

func (w *Worker) handleVisibilityRun(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	if !job.AuditID.Valid {
		return fmt.Errorf("visibility_run job %s has no audit_id", job.ID.String())
	}

	paq, err := w.queries.GetProjectAIQuestions(ctx, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no ai questions for project %s", job.ProjectID.String())
		}
		return fmt.Errorf("load ai questions: %w", err)
	}

	var questions []string
	if err := json.Unmarshal(paq.Questions, &questions); err != nil {
		return fmt.Errorf("decode questions: %w", err)
	}
	if len(questions) == 0 {
		return fmt.Errorf("no questions for project %s", job.ProjectID.String())
	}

	profile, err := w.queries.GetProjectBusinessProfileByProjectID(ctx, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no business profile for project %s", job.ProjectID.String())
		}
		return fmt.Errorf("load business profile: %w", err)
	}
	businessName := profile.BrandName

	auditID := pgtype.UUID{Bytes: job.AuditID.Bytes, Valid: true}

	startedAt := time.Now()
	if updateErr := w.queries.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{
		ID:           auditID,
		Status:       "running",
		ErrorMessage: pgtype.Text{},
		StartedAt:    pgtype.Timestamptz{Time: startedAt, Valid: true},
		CompletedAt:  pgtype.Timestamptz{},
	}); updateErr != nil {
		return fmt.Errorf("mark audit running: %w", updateErr)
	}

	models := w.cfg.AIVisibilityModels
	if len(models) == 0 {
		return fmt.Errorf("no AI visibility models configured")
	}

	var mu sync.Mutex
	failCount := 0
	totalCount := len(questions) * len(models)

	// One goroutine per model; questions are serialized within each model
	// to respect per-model rate limits (configurable via AI_VISIBILITY_RATE_DELAY).
	var wg sync.WaitGroup
	for _, modelSlug := range models {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			for i, question := range questions {
				if ctx.Err() != nil {
					return
				}
				runErr := w.runSingleVisibilityCheck(ctx, auditID, i+1, question, slug, businessName)
				if runErr != nil {
					mu.Lock()
					failCount++
					mu.Unlock()
					log.Printf("visibility run: question %d model %s failed: %v", i+1, slug, runErr)
				}
				if i < len(questions)-1 && w.cfg.AIVisibilityRateDelay > 0 {
					if sleepErr := sleepOrCancel(ctx, w.cfg.AIVisibilityRateDelay); sleepErr != nil {
						return
					}
				}
			}
		}(modelSlug)
	}
	wg.Wait()

	finalStatus := "completed"
	if failCount == totalCount {
		finalStatus = "failed"
	} else if failCount > 0 {
		finalStatus = "completed_with_failures"
	}

	if updateErr := w.queries.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{
		ID:          auditID,
		Status:      finalStatus,
		StartedAt:   pgtype.Timestamptz{Time: startedAt, Valid: true},
		CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); updateErr != nil {
		return fmt.Errorf("mark audit %s: %w", finalStatus, updateErr)
	}

	return nil
}

func (w *Worker) runSingleVisibilityCheck(ctx context.Context, auditID pgtype.UUID, displayOrder int, questionText, modelSlug, businessName string) error {
	provider, err := ai.NewProvider(ai.ProviderConfig{
		Name:   "openrouter",
		APIKey: w.cfg.OpenRouterAPIKey,
		Model:  modelSlug,
	})
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}

	prompt := "List the top businesses or services for the following question.\n" +
		"Respond with only a numbered list (1. 2. 3. etc.), no explanations, no intro text, no filler.\n\n" +
		"Question: " + questionText

	startedAt := time.Now()

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rawResponse, callErr := provider.GenerateText(callCtx, prompt)
	cancel()

	if callErr != nil && (strings.Contains(callErr.Error(), "429") || strings.Contains(strings.ToLower(callErr.Error()), "rate limit")) {
		time.Sleep(5 * time.Second)
		callCtx2, cancel2 := context.WithTimeout(ctx, 30*time.Second)
		rawResponse, callErr = provider.GenerateText(callCtx2, prompt)
		cancel2()
	}

	completedAt := time.Now()

	if callErr != nil {
		_, insertErr := w.queries.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
			AuditID:         auditID,
			QuestionText:    questionText,
			DisplayOrder:    int32(displayOrder),
			ModelName:       modelSlug,
			Status:          "failed",
			RawResponse:     pgtype.Text{},
			MentionedTarget: pgtype.Bool{Bool: false, Valid: true},
			TargetRank:      pgtype.Int4{},
			VisibilityScore: pgtype.Int4{},
			ErrorMessage:    pgtype.Text{String: callErr.Error(), Valid: true},
			StartedAt:       pgtype.Timestamptz{Time: startedAt, Valid: true},
			CompletedAt:     pgtype.Timestamptz{Time: completedAt, Valid: true},
		})
		if insertErr != nil {
			return fmt.Errorf("insert failed run: %w", insertErr)
		}
		return callErr
	}

	mentioned, rank, score := parseVisibilityResponse(rawResponse, businessName)

	_, insertErr := w.queries.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
		AuditID:         auditID,
		QuestionText:    questionText,
		DisplayOrder:    int32(displayOrder),
		ModelName:       modelSlug,
		Status:          "success",
		RawResponse:     pgtype.Text{String: rawResponse, Valid: true},
		MentionedTarget: pgtype.Bool{Bool: mentioned, Valid: true},
		TargetRank:      pgtype.Int4{Int32: int32(rank), Valid: rank > 0},
		VisibilityScore: pgtype.Int4{Int32: int32(score), Valid: true},
		ErrorMessage:    pgtype.Text{},
		StartedAt:       pgtype.Timestamptz{Time: startedAt, Valid: true},
		CompletedAt:     pgtype.Timestamptz{Time: completedAt, Valid: true},
	})
	if insertErr != nil {
		return fmt.Errorf("insert run: %w", insertErr)
	}

	return nil
}

func parseVisibilityResponse(response, businessName string) (mentioned bool, rank int, score int) {
	lowerBusiness := strings.ToLower(businessName)
	matches := numberedItemRe.FindAllStringSubmatch(response, -1)

	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		item := strings.ToLower(match[1])
		if strings.Contains(item, lowerBusiness) {
			n := i + 1
			s := 100 - (n-1)*10
			if s < 0 {
				s = 0
			}
			return true, n, s
		}
	}

	// Fall back to full-text scan if no numbered match found
	if strings.Contains(strings.ToLower(response), lowerBusiness) {
		return true, 0, 10
	}

	return false, 0, 0
}
