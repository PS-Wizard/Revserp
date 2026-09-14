package app

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// handleRegenerateProjectAIQuestions re-enqueues the prompt generation job.
// Used when a project predates location questions or a generation pass failed:
// the business profile frontend exposes this as a "Regenerate" action.
func (a *App) handleRegenerateProjectAIQuestions(w http.ResponseWriter, r *http.Request) {
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

	// The generation pass requires a saved profile; without one the job would
	// fail in the worker with a confusing error.
	if _, err := queries.GetProjectBusinessProfileByProjectID(r.Context(), projectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "save the business profile before regenerating questions")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.EnqueueAIWorkerJob(r.Context(), sqlc.EnqueueAIWorkerJobParams{
		JobType:   "prompt_generation",
		ProjectID: projectID,
	}); err != nil {
		log.Printf("enqueue prompt_generation (regenerate) for project %s: %v", projectID.String(), err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}
