package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type projectAIQuestionsResponse struct {
	Questions       []string `json:"questions"`
	GenerationModel string   `json:"generation_model"`
	GeneratedAt     string   `json:"generated_at"`
}

// handleGetProjectAIQuestions returns the AI-generated questions for a project.
func (a *App) handleGetProjectAIQuestions(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	row, err := queries.GetProjectAIQuestions(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "no ai questions generated yet")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	var questions []string
	if len(row.Questions) > 0 {
		if err := json.Unmarshal(row.Questions, &questions); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	if questions == nil {
		questions = []string{}
	}

	writeJSON(w, http.StatusOK, projectAIQuestionsResponse{
		Questions:       questions,
		GenerationModel: row.GenerationModel,
		GeneratedAt:     row.GeneratedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	})
}
