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
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

var numberedItemRe = regexp.MustCompile(`(?m)^\s*\d+[.)]\s+(.+)`)

type visibilityQueries interface {
	GetAIAuditForWorker(ctx context.Context, arg sqlc.GetAIAuditForWorkerParams) (sqlc.AiAudit, error)
	GetLocationForAIAuditWorker(ctx context.Context, arg sqlc.GetLocationForAIAuditWorkerParams) (sqlc.ProjectLocation, error)
	GetProjectAIQuestions(ctx context.Context, projectID pgtype.UUID) (sqlc.GetProjectAIQuestionsRow, error)
	GetProjectBusinessProfileByProjectID(ctx context.Context, projectID pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, error)
	UpdateAIAuditStatus(ctx context.Context, arg sqlc.UpdateAIAuditStatusParams) error
	InsertAIAuditRun(ctx context.Context, arg sqlc.InsertAIAuditRunParams) (sqlc.AiAuditRun, error)
	MarkAIWorkerJobFailed(ctx context.Context, arg sqlc.MarkAIWorkerJobFailedParams) error
}

func (w *Worker) visibilityStore() visibilityQueries {
	if w.visibilityQueries != nil {
		return w.visibilityQueries
	}
	return w.queries
}

func (w *Worker) visibilityProvider(modelSlug string) (ai.Provider, error) {
	if w.newVisibilityProvider != nil {
		return w.newVisibilityProvider(modelSlug)
	}
	return ai.NewProvider(ai.ProviderConfig{
		Name:   "openrouter",
		APIKey: w.cfg.OpenRouterAPIKey,
		Model:  modelSlug,
	})
}

// LocationBusinessProfile is a location's independent copy of profile facts.
// Only this copy feeds a location run: a parent profile edit never changes it.
type LocationBusinessProfile struct {
	BrandName           string
	WebsiteURL          string
	PrimaryCategory     string
	PrimaryLocation     string
	BusinessDescription string
	ProductDescription  string
	TargetAudience      string
	BusinessCompetitors []string
	Services            []string
	SeedPrompts         []string
}

// LocationProfileReader loads the independent profile copy for one location.
type LocationProfileReader interface {
	GetLocationBusinessProfile(ctx context.Context, locationID, projectID pgtype.UUID) (LocationBusinessProfile, error)
}

// LocationProfileStore reads location_business_profiles with pgx.
type LocationProfileStore struct {
	Pool locationProfileRowQuerier
}

type locationProfileRowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// GetLocationBusinessProfile returns the copied profile scoped to its owning
// project. pgx.ErrNoRows means the location has no profile copy yet.
func (s LocationProfileStore) GetLocationBusinessProfile(ctx context.Context, locationID, projectID pgtype.UUID) (LocationBusinessProfile, error) {
	var (
		profile                            LocationBusinessProfile
		competitors, services, seedPrompts []byte
	)
	err := s.Pool.QueryRow(ctx,
		`SELECT brand_name, website_url,
		 COALESCE(primary_category,''), COALESCE(primary_location,''),
		 COALESCE(business_description,''), COALESCE(product_description,''),
		 COALESCE(target_audience,''), business_competitors, services, seed_prompts
		 FROM location_business_profiles WHERE location_id = $1 AND project_id = $2`,
		locationID, projectID,
	).Scan(&profile.BrandName, &profile.WebsiteURL, &profile.PrimaryCategory, &profile.PrimaryLocation,
		&profile.BusinessDescription, &profile.ProductDescription, &profile.TargetAudience,
		&competitors, &services, &seedPrompts)
	if err != nil {
		return LocationBusinessProfile{}, err
	}
	if profile.BusinessCompetitors, err = decodeJSONStringArray(competitors); err != nil {
		return LocationBusinessProfile{}, fmt.Errorf("decode location competitors: %w", err)
	}
	if profile.Services, err = decodeJSONStringArray(services); err != nil {
		return LocationBusinessProfile{}, fmt.Errorf("decode location services: %w", err)
	}
	if profile.SeedPrompts, err = decodeJSONStringArray(seedPrompts); err != nil {
		return LocationBusinessProfile{}, fmt.Errorf("decode location seed prompts: %w", err)
	}
	return profile, nil
}

// decodeJSONStringArray decodes a jsonb string array, treating null/empty as nil.
func decodeJSONStringArray(raw []byte) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func (w *Worker) locationProfileReader() LocationProfileReader {
	if w.locationProfiles != nil {
		return w.locationProfiles
	}
	return LocationProfileStore{Pool: w.pool}
}

func (w *Worker) isLocationVisibilityJob(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) (bool, error) {
	if !job.AuditID.Valid {
		return false, nil
	}
	audit, err := w.visibilityStore().GetAIAuditForWorker(ctx, sqlc.GetAIAuditForWorkerParams{
		ID:        pgtype.UUID{Bytes: job.AuditID.Bytes, Valid: true},
		ProjectID: job.ProjectID,
	})
	if err != nil {
		return false, fmt.Errorf("load visibility audit scope: %w", err)
	}
	return audit.LocationID.Valid, nil
}

func (w *Worker) failLocationVisibilityJob(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow, message string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin location visibility failure: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := failLocationVisibilityAuditTx(ctx, w.queries.WithTx(tx), job, message); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit location visibility failure: %w", err)
	}
	return nil
}

func failLocationVisibilityAuditTx(ctx context.Context, store visibilityQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, message string) error {
	capped := capSetupErrorMessage(message)
	markJob := func() error {
		return store.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{
			ID:           job.ID,
			ErrorMessage: pgtype.Text{String: capped, Valid: true},
		})
	}
	if !job.AuditID.Valid {
		return markJob()
	}
	audit, err := store.GetAIAuditForWorker(ctx, sqlc.GetAIAuditForWorkerParams{
		ID:        pgtype.UUID{Bytes: job.AuditID.Bytes, Valid: true},
		ProjectID: job.ProjectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return markJob()
		}
		return fmt.Errorf("load location audit for failure: %w", err)
	}
	if audit.LocationID.Valid && (audit.Status == "queued" || audit.Status == "running") {
		if err := store.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{
			ID:           audit.ID,
			Status:       "failed",
			ErrorMessage: pgtype.Text{String: capped, Valid: true},
			StartedAt:    audit.StartedAt,
			CompletedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			return fmt.Errorf("fail location audit: %w", err)
		}
	}
	return markJob()
}

func (w *Worker) handleVisibilityRun(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) (string, error) {
	if !job.AuditID.Valid {
		return "", fmt.Errorf("visibility_run job %s has no audit_id", job.ID.String())
	}
	store := w.visibilityStore()
	auditID := pgtype.UUID{Bytes: job.AuditID.Bytes, Valid: true}
	audit, err := store.GetAIAuditForWorker(ctx, sqlc.GetAIAuditForWorkerParams{
		ID:        auditID,
		ProjectID: job.ProjectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no visibility audit %s for project %s", auditID.String(), job.ProjectID.String())
		}
		return "", fmt.Errorf("load visibility audit: %w", err)
	}
	if audit.LocationID.Valid {
		return w.handleLocationVisibilityRun(ctx, store, job, audit)
	}
	return w.handleProjectVisibilityRun(ctx, store, job, auditID)
}

func (w *Worker) handleProjectVisibilityRun(ctx context.Context, store visibilityQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, auditID pgtype.UUID) (string, error) {
	paq, err := store.GetProjectAIQuestions(ctx, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no ai questions for project %s", job.ProjectID.String())
		}
		return "", fmt.Errorf("load ai questions: %w", err)
	}

	var questions []string
	if err := json.Unmarshal(paq.Questions, &questions); err != nil {
		return "", fmt.Errorf("decode questions: %w", err)
	}
	if len(questions) == 0 {
		return "", fmt.Errorf("no questions for project %s", job.ProjectID.String())
	}

	profile, err := store.GetProjectBusinessProfileByProjectID(ctx, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no business profile for project %s", job.ProjectID.String())
		}
		return "", fmt.Errorf("load business profile: %w", err)
	}

	items := make([]visibilityQuestion, len(questions))
	for i, question := range questions {
		items[i] = visibilityQuestion{order: i + 1, text: question}
	}
	return w.runVisibilityQuestions(ctx, store, auditID, items, profile.BrandName)
}

func (w *Worker) handleLocationVisibilityRun(ctx context.Context, store visibilityQueries, job sqlc.ClaimNextPendingAIWorkerJobRow, audit sqlc.AiAudit) (string, error) {
	location, err := store.GetLocationForAIAuditWorker(ctx, sqlc.GetLocationForAIAuditWorkerParams{
		LocationID: audit.LocationID,
		ProjectID:  job.ProjectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no location %s for project %s", audit.LocationID.String(), job.ProjectID.String())
		}
		return "", fmt.Errorf("load visibility location: %w", err)
	}

	profile, err := w.locationProfileReader().GetLocationBusinessProfile(ctx, audit.LocationID, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("no location business profile for %s", audit.LocationID.String())
		}
		return "", fmt.Errorf("load location business profile: %w", err)
	}

	questions, err := w.locationQuestionReader().GetLocationAIQuestions(ctx, audit.LocationID, job.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load location ai questions: %w", err)
	}
	if len(questions) == 0 {
		return "", fmt.Errorf("location %s has no AI questions", audit.LocationID.String())
	}

	items := make([]visibilityQuestion, len(questions))
	for i, question := range questions {
		items[i] = visibilityQuestion{order: i + 1, text: question, officeName: location.Name}
	}
	return w.runVisibilityQuestions(ctx, store, audit.ID, items, profile.BrandName)
}

type visibilityQuestion struct {
	order      int
	text       string
	officeName string
}

func (w *Worker) runVisibilityQuestions(ctx context.Context, store visibilityQueries, auditID pgtype.UUID, items []visibilityQuestion, businessName string) (string, error) {
	startedAt := time.Now()
	if updateErr := store.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{
		ID:           auditID,
		Status:       "running",
		ErrorMessage: pgtype.Text{},
		StartedAt:    pgtype.Timestamptz{Time: startedAt, Valid: true},
		CompletedAt:  pgtype.Timestamptz{},
	}); updateErr != nil {
		return "", fmt.Errorf("mark audit running: %w", updateErr)
	}

	models := w.cfg.AIVisibilityModels
	if len(models) == 0 {
		return "", fmt.Errorf("no AI visibility models configured")
	}

	var mu sync.Mutex
	failCount := 0
	totalCount := len(items) * len(models)

	// One goroutine per model; questions are serialized within each model
	// to respect per-model rate limits (configurable via AI_VISIBILITY_RATE_DELAY).
	var wg sync.WaitGroup
	for _, modelSlug := range models {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			for i, item := range items {
				if ctx.Err() != nil {
					return
				}
				runErr := w.runSingleVisibilityCheck(ctx, store, auditID, item.order, item.text, slug, businessName, item.officeName)
				if runErr != nil {
					mu.Lock()
					failCount++
					mu.Unlock()
					log.Printf("visibility run: question %d model %s failed: %v", item.order, slug, runErr)
				}
				if i < len(items)-1 && w.cfg.AIVisibilityRateDelay > 0 {
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

	if updateErr := store.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{
		ID:          auditID,
		Status:      finalStatus,
		StartedAt:   pgtype.Timestamptz{Time: startedAt, Valid: true},
		CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); updateErr != nil {
		return "", fmt.Errorf("mark audit %s: %w", finalStatus, updateErr)
	}

	return finalStatus, nil
}

func (w *Worker) runSingleVisibilityCheck(ctx context.Context, store visibilityQueries, auditID pgtype.UUID, displayOrder int, questionText, modelSlug, businessName, officeName string) error {
	provider, err := w.visibilityProvider(modelSlug)
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
		_, insertErr := store.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
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
	branch := pgtype.Bool{}
	if officeName != "" {
		branch = pgtype.Bool{Bool: detectMentionedBranch(rawResponse, officeName, businessName, questionText), Valid: true}
	}

	_, insertErr := store.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
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
		MentionedBranch: branch,
	})
	if insertErr != nil {
		return fmt.Errorf("insert run: %w", insertErr)
	}

	return nil
}

func detectMentionedBranch(answer, officeName, brandName, queryText string) bool {
	office := normalizeBranchName(officeName)
	if office == "" {
		return false
	}
	if brand := normalizeBranchName(brandName); brand != "" {
		if office == brand || strings.HasPrefix(brand, office) {
			return false
		}
	}
	query := normalizeBranchName(queryText)
	mentions := func(text string) bool {
		for _, sentence := range splitBranchSentences(text) {
			normalized := normalizeBranchName(sentence)
			if query != "" {
				normalized = strings.ReplaceAll(normalized, query, " ")
			}
			if strings.Contains(" "+normalized+" ", " "+office+" ") {
				return true
			}
		}
		return false
	}
	if matches := numberedItemRe.FindAllStringSubmatch(answer, -1); len(matches) > 0 {
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			if mentions(match[1]) {
				return true
			}
		}
		return false
	}
	return mentions(answer)
}

func splitBranchSentences(answer string) []string {
	return strings.FieldsFunc(answer, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	})
}

func normalizeBranchName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	spaced := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			spaced = false
			continue
		}
		if !spaced {
			b.WriteByte(' ')
			spaced = true
		}
	}
	return strings.TrimSpace(b.String())
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
