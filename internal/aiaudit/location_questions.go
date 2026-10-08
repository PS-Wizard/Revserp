package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// LocationAIQuestionReader reads and writes the independent per-location AI
// question set. Parent project_ai_questions is never read or written here.
type LocationAIQuestionReader interface {
	GetLocationAIQuestions(ctx context.Context, locationID, projectID pgtype.UUID) ([]string, error)
	UpsertLocationAIQuestions(ctx context.Context, locationID, projectID pgtype.UUID, questions []string, model string) error
}

type LocationQuestionStore struct {
	Pool locationQuestionPool
}

type locationQuestionPool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// GetLocationAIQuestions returns the saved local questions, or nil when the
// location has never generated or edited a set.
func (s LocationQuestionStore) GetLocationAIQuestions(ctx context.Context, locationID, projectID pgtype.UUID) ([]string, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT questions FROM location_ai_questions WHERE location_id = $1 AND project_id = $2`,
		locationID, projectID,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSONStringArray(raw)
}

func (s LocationQuestionStore) UpsertLocationAIQuestions(ctx context.Context, locationID, projectID pgtype.UUID, questions []string, model string) error {
	encoded, err := json.Marshal(questions)
	if err != nil {
		return fmt.Errorf("encode location questions: %w", err)
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO location_ai_questions (project_id, location_id, questions, generation_model)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (location_id) DO UPDATE SET
		     questions = EXCLUDED.questions,
		     generation_model = EXCLUDED.generation_model,
		     generated_at = now(),
		     updated_at = now()`,
		projectID, locationID, encoded, model,
	)
	return err
}

func (w *Worker) locationQuestionReader() LocationAIQuestionReader {
	if w.locationQuestions != nil {
		return w.locationQuestions
	}
	return LocationQuestionStore{Pool: w.pool}
}

func (w *Worker) promptProvider() (ai.Provider, error) {
	if w.newPromptProvider != nil {
		return w.newPromptProvider()
	}
	model := w.cfg.DeepSeekModel
	if model == "" {
		model = "deepseek-flash"
	}
	return ai.NewProvider(ai.ProviderConfig{
		Name:   "deepseek",
		APIKey: w.cfg.DeepSeekAPIKey,
		Model:  model,
	})
}

// isLocationPromptJob reports whether a prompt_generation job is location
// scoped, so setup finalization is skipped for it. The scope comes from the
// claimed row, so a deleted or unknown job row can never fall back to parent setup.
func (w *Worker) isLocationPromptJob(job sqlc.ClaimNextPendingAIWorkerJobRow) bool {
	return job.LocationID.Valid
}

// LocationTarget is the bound listing identity used for local targeting. It is
// read from the saved location row; the copied profile is never mutated.
type LocationTarget struct {
	Name       string
	Address    string
	Locality   string
	Localities []string
}

type LocationTargetReader interface {
	GetLocationTarget(ctx context.Context, locationID, projectID pgtype.UUID) (LocationTarget, error)
}

type LocationTargetStore struct {
	Pool locationQuestionPool
}

func (s LocationTargetStore) GetLocationTarget(ctx context.Context, locationID, projectID pgtype.UUID) (LocationTarget, error) {
	var (
		target     LocationTarget
		localities []byte
	)
	err := s.Pool.QueryRow(ctx,
		`SELECT name, address, locality, localities FROM project_locations WHERE id = $1 AND project_id = $2`,
		locationID, projectID,
	).Scan(&target.Name, &target.Address, &target.Locality, &localities)
	if err != nil {
		return LocationTarget{}, err
	}
	if target.Localities, err = decodeJSONStringArray(localities); err != nil {
		return LocationTarget{}, fmt.Errorf("decode location localities: %w", err)
	}
	return target, nil
}

func (w *Worker) locationTargetReader() LocationTargetReader {
	if w.locationTargets != nil {
		return w.locationTargets
	}
	return LocationTargetStore{Pool: w.pool}
}

// LocationLandmark is one saved nearby place the location already knows.
// Only stored rows feed question generation: nothing here discovers places.
type LocationLandmark struct {
	Name          string
	StraightLineM int32
	Categories    []string
}

type LocationLandmarkReader interface {
	GetLocationLandmarks(ctx context.Context, locationID, projectID pgtype.UUID) ([]LocationLandmark, error)
}

// LocationLandmarkStore reads location_landmarks with pgx. An empty table
// reads as no landmarks, never as a discovery request.
type LocationLandmarkStore struct {
	Pool locationLandmarkPool
}

type locationLandmarkPool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// GetLocationLandmarks returns every saved nearby place for the location in
// stable name order.
func (s LocationLandmarkStore) GetLocationLandmarks(ctx context.Context, locationID, projectID pgtype.UUID) ([]LocationLandmark, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT name, straight_line_m, categories FROM location_landmarks WHERE location_id = $1 AND location_id IN (SELECT id FROM project_locations WHERE id = $1 AND project_id = $2) ORDER BY name, id`,
		locationID, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocationLandmark
	for rows.Next() {
		var landmark LocationLandmark
		if err := rows.Scan(&landmark.Name, &landmark.StraightLineM, &landmark.Categories); err != nil {
			return nil, err
		}
		out = append(out, landmark)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (w *Worker) locationLandmarkReader() LocationLandmarkReader {
	if w.locationLandmarks != nil {
		return w.locationLandmarks
	}
	return LocationLandmarkStore{Pool: w.pool}
}

func (w *Worker) handleLocationPromptGeneration(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	locationID := job.LocationID
	if !locationID.Valid {
		return fmt.Errorf("prompt job %s has no location scope", job.ID.String())
	}
	profile, err := w.locationProfileReader().GetLocationBusinessProfile(ctx, locationID, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no location business profile for %s", locationID.String())
		}
		return fmt.Errorf("load location business profile: %w", err)
	}
	target, err := w.locationTargetReader().GetLocationTarget(ctx, locationID, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load location target: %w", err)
	}
	landmarks, err := w.locationLandmarkReader().GetLocationLandmarks(ctx, locationID, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load location landmarks: %w", err)
	}

	provider, err := w.promptProvider()
	if err != nil {
		return fmt.Errorf("create ai provider: %w", err)
	}

	raw, err := provider.GenerateText(ctx, buildLocationGenerationPrompt(DefaultQuestionGenerationPrompt, profile, target, landmarks))
	if err != nil {
		return fmt.Errorf("generate location questions: %w", err)
	}
	questions := parseGeneratedQuestions(raw)
	if len(questions) == 0 {
		return fmt.Errorf("ai returned no parseable location questions")
	}

	model := w.cfg.DeepSeekModel
	if model == "" {
		model = "deepseek-flash"
	}
	if err := w.locationQuestionReader().UpsertLocationAIQuestions(ctx, locationID, job.ProjectID, questions, model); err != nil {
		return fmt.Errorf("save location questions: %w", err)
	}
	return nil
}

// buildLocationGenerationPrompt renders the shared discovery-question prompt
// against the location's independent profile copy. The bound listing identity
// is authoritative for local targeting so a copied profile country never
// overrides the actual saved location.
func buildLocationGenerationPrompt(systemPrompt string, profile LocationBusinessProfile, target LocationTarget, landmarks []LocationLandmark) string {
	var sb strings.Builder
	sb.WriteString(systemPrompt)
	sb.WriteString("\n\n---\nBusiness Profile:\n")
	sb.WriteString("Brand: ")
	sb.WriteString(profile.BrandName)
	if profile.WebsiteURL != "" {
		sb.WriteString("\nWebsite: ")
		sb.WriteString(profile.WebsiteURL)
	}
	if profile.PrimaryCategory != "" {
		sb.WriteString("\nCategory: ")
		sb.WriteString(profile.PrimaryCategory)
	}
	if profile.PrimaryLocation != "" {
		sb.WriteString("\nLocation: ")
		sb.WriteString(profile.PrimaryLocation)
	}
	if profile.BusinessDescription != "" {
		sb.WriteString("\nDescription: ")
		sb.WriteString(profile.BusinessDescription)
	}
	if profile.ProductDescription != "" {
		sb.WriteString("\nProducts: ")
		sb.WriteString(profile.ProductDescription)
	}
	if profile.TargetAudience != "" {
		sb.WriteString("\nAudience: ")
		sb.WriteString(profile.TargetAudience)
	}
	if len(profile.BusinessCompetitors) > 0 {
		sb.WriteString("\nCompetitors: ")
		sb.WriteString(strings.Join(profile.BusinessCompetitors, ", "))
	}
	if len(profile.Services) > 0 {
		sb.WriteString("\nServices: ")
		sb.WriteString(strings.Join(profile.Services, ", "))
	}
	if hasBoundLocation(target) {
		sb.WriteString("\n\nBound location (authoritative local target; never substitute a country or region from the profile):\n")
		if target.Name != "" {
			sb.WriteString("Name: ")
			sb.WriteString(target.Name)
			sb.WriteString("\n")
		}
		if target.Address != "" {
			sb.WriteString("Address: ")
			sb.WriteString(target.Address)
			sb.WriteString("\n")
		}
		if target.Locality != "" {
			sb.WriteString("Locality: ")
			sb.WriteString(target.Locality)
			sb.WriteString("\n")
		}
		if len(target.Localities) > 0 {
			sb.WriteString("Localities: ")
			sb.WriteString(strings.Join(target.Localities, ", "))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n\nNearby buyer-intent rules (location questions must stay local even with no seeds):\n")
	sb.WriteString("Ground every question in the bound location and services above. When the profile Location names a country or region, it is background only; the bound locality is the question geography. Prefer a buyer-intent near/in phrasing with the bound locality so empty seeds still yield local questions.\n")
	if len(landmarks) > 0 {
		sb.WriteString("\nSaved nearby landmarks (real places near this location; reference at most two of them naturally in near-me buyer questions; never invent other place names):\n")
		for _, landmark := range landmarks {
			fmt.Fprintf(&sb, "- %s", landmark.Name)
			if landmark.StraightLineM > 0 {
				fmt.Fprintf(&sb, " (%.1f km)", float64(landmark.StraightLineM)/1000)
			}
			if len(landmark.Categories) > 0 {
				fmt.Fprintf(&sb, " [%s]", strings.Join(landmark.Categories, ", "))
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("\nSaved nearby landmarks: none recorded. Do not invent place names.\n")
	}
	if len(profile.SeedPrompts) > 0 {
		sb.WriteString("\nSeed questions:\n")
		for i, p := range profile.SeedPrompts {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, p)
		}
	} else {
		sb.WriteString("\nSeed questions: none saved. Use the bound locality and services above as the question geography.\n")
	}
	sb.WriteString("\nGenerate 10 questions:")
	return sb.String()
}

func hasBoundLocation(target LocationTarget) bool {
	return target.Name != "" || target.Address != "" || target.Locality != "" || len(target.Localities) > 0
}
