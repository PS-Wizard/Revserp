package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// locationAIQuestionsResponse mirrors the project AI questions shape so the
// frontend can reuse its type. location_questions stays empty for a location:
// its Maps keywords live in the keyword layer, not here.
type locationAIQuestionsResponse struct {
	Questions         []string `json:"questions"`
	LocationQuestions []string `json:"location_questions"`
	GenerationModel   string   `json:"generation_model"`
	GeneratedAt       string   `json:"generated_at"`
}

type putLocationAIQuestionsRequest struct {
	Questions []string `json:"questions"`
}

// maxLocationAIQuestions bounds the editable local question set.
const maxLocationAIQuestions = 20

// handleGetLocationAIQuestions returns the location's independently saved AI
// questions. An absent set reads as empty, never as the parent's questions.
func (a *App) handleGetLocationAIQuestions(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	response, err := a.loadLocationAIQuestions(r.Context(), location.ProjectID, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handlePutLocationAIQuestions stores an explicit local question edit. Owner
// only. It never writes the parent project_ai_questions row.
func (a *App) handlePutLocationAIQuestions(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	var body putLocationAIQuestionsRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	questions, err := normalizeLocationAIQuestions(body.Questions)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	encoded, err := json.Marshal(questions)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := a.DB.Exec(r.Context(),
		`INSERT INTO location_ai_questions (project_id, location_id, questions, generation_model)
		 VALUES ($1, $2, $3, '')
		 ON CONFLICT (location_id) DO UPDATE SET questions = EXCLUDED.questions, updated_at = now()`,
		location.ProjectID, location.ID, encoded,
	); err != nil {
		serverError(w, r, err)
		return
	}
	response, err := a.loadLocationAIQuestions(r.Context(), location.ProjectID, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleRegenerateLocationAIQuestions enqueues one explicit location question
// generation job. Owner only; nothing runs automatically on profile save.
func (a *App) handleRegenerateLocationAIQuestions(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	var hasProfile bool
	if err := a.DB.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM location_business_profiles WHERE location_id = $1 AND project_id = $2)`,
		location.ID, location.ProjectID,
	).Scan(&hasProfile); err != nil {
		serverError(w, r, err)
		return
	}
	if !hasProfile {
		writeJSONError(w, http.StatusBadRequest, "save the business profile first")
		return
	}
	if _, err := a.DB.Exec(r.Context(),
		`INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status) VALUES ('prompt_generation', $1, $2, 'pending') ON CONFLICT DO NOTHING`,
		location.ProjectID, location.ID,
	); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// loadLocationAIQuestions reads the saved local question set.
func (a *App) loadLocationAIQuestions(ctx context.Context, projectID, locationID pgtype.UUID) (locationAIQuestionsResponse, error) {
	var (
		raw         []byte
		model       string
		generatedAt pgtype.Timestamptz
	)
	err := a.DB.QueryRow(ctx,
		`SELECT questions, generation_model, generated_at FROM location_ai_questions WHERE location_id = $1 AND project_id = $2`,
		locationID, projectID,
	).Scan(&raw, &model, &generatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return locationAIQuestionsResponse{Questions: []string{}, LocationQuestions: []string{}}, nil
		}
		return locationAIQuestionsResponse{}, err
	}
	var questions []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &questions); err != nil {
			return locationAIQuestionsResponse{}, err
		}
	}
	if questions == nil {
		questions = []string{}
	}
	response := locationAIQuestionsResponse{
		Questions:         questions,
		LocationQuestions: []string{},
		GenerationModel:   model,
	}
	if generatedAt.Valid {
		response.GeneratedAt = formatTimestamp(generatedAt)
	}
	return response, nil
}

// locationHasAIQuestions reports whether the location has a non-empty saved
// question set, used to gate a local visibility run before charge/queue.
func (a *App) locationHasAIQuestions(ctx context.Context, projectID, locationID pgtype.UUID) (bool, error) {
	var has bool
	err := a.DB.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM location_ai_questions WHERE location_id = $1 AND project_id = $2 AND jsonb_array_length(questions) > 0)`,
		locationID, projectID,
	).Scan(&has)
	return has, err
}

// normalizeLocationAIQuestions trims, drops blanks and case-insensitive
// duplicates, preserving first spelling, and caps the set.
func normalizeLocationAIQuestions(raw []string) ([]string, error) {
	questions := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, question := range raw {
		trimmed := strings.TrimSpace(question)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		questions = append(questions, trimmed)
		if len(questions) == maxLocationAIQuestions {
			break
		}
	}
	if len(questions) == 0 {
		return nil, errors.New("at least one question is required")
	}
	return questions, nil
}
