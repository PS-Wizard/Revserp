package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiQuestionsGenerationStatusResponse struct {
	Status      string  `json:"status"`
	Error       string  `json:"error"`
	RequestedAt string  `json:"requested_at"`
	CompletedAt *string `json:"completed_at"`
}

// handleGetAIQuestionsGenerationStatus reports the latest prompt_generation
// job for a project, so the UI can turn "Regenerating questions…" into a real
// success or failure instead of assuming success.
func (a *App) handleGetAIQuestionsGenerationStatus(w http.ResponseWriter, r *http.Request) {
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
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
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

	job, err := queries.GetLatestPromptGenerationJobByProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No generation ever requested for this project — a status, not an
			// error, so the caller needs no extra branch.
			writeJSON(w, http.StatusOK, aiQuestionsGenerationStatusResponse{
				Status: "none",
			})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response := aiQuestionsGenerationStatusResponse{
		Status:      job.Status,
		RequestedAt: job.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if job.ErrorMessage.Valid {
		response.Error = job.ErrorMessage.String
	}
	if job.CompletedAt.Valid {
		completed := job.CompletedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
		response.CompletedAt = &completed
	}

	writeJSON(w, http.StatusOK, response)
}
