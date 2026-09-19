package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

// Project setup workflow states, mirroring the project_setup.status check
// constraint. Project creation inserts the row in "ready"; the owner's POST
// starts it in "crawling" and workers advance it through the remaining states.
const (
	projectSetupStatusReady             = "ready"
	projectSetupStatusCrawling          = "crawling"
	projectSetupStatusProfileGeneration = "profile_generation"
	projectSetupStatusPromptGeneration  = "prompt_generation"
	projectSetupStatusCompleted         = "completed"
	projectSetupStatusFailed            = "failed"
	projectSetupCrawlSource             = "manual"
)

type projectSetupResponse struct {
	ID                   string `json:"id"`
	OrganizationID       string `json:"organization_id"`
	ProjectID            string `json:"project_id"`
	RequestedByUserID    string `json:"requested_by_user_id"`
	Status               string `json:"status"`
	CrawlID              string `json:"crawl_id,omitempty"`
	Error                string `json:"error,omitempty"`
	FailedStep           string `json:"failed_step,omitempty"`
	VisibilitySkipReason string `json:"visibility_skip_reason,omitempty"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
	CompletedAt          string `json:"completed_at,omitempty"`
}

// newReadyProjectSetupParams is the initial setup row every newly created
// project gets. It starts in the pre-start "ready" status, so no worker runs
// and the setup SSE trigger emits no event until the owner starts it.
func newReadyProjectSetupParams(organizationID, projectID, userID pgtype.UUID) sqlc.CreateProjectSetupParams {
	return sqlc.CreateProjectSetupParams{
		OrganizationID:    organizationID,
		ProjectID:         projectID,
		RequestedByUserID: userID,
		Status:            projectSetupStatusReady,
	}
}

// handleGetProjectSetup returns the durable setup row for any project member.
// A freshly created project returns its pre-start "ready" row; 404 only for a
// project that predates setup (no row), matching the maps-visibility convention.
func (a *App) handleGetProjectSetup(w http.ResponseWriter, r *http.Request) {
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

	setup, err := queries.GetProjectSetupByProjectID(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project setup not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, newProjectSetupResponse(setup))
}

// handleStartProjectSetup is the owner-only, idempotent setup start. It locks
// the project's setup row and:
//   - ready: links the first crawl and moves to crawling, returning 201;
//   - failed: resumes atomically at failed_step, returning 200;
//   - active or completed: returns the row unchanged and enqueues nothing (200).
//
// A project with no setup row predates the durable workflow and stays on manual
// flows: the POST returns 404 and never creates a row.
func (a *App) handleStartProjectSetup(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	normalizedConfigSnapshot, err := normalizeCreateCrawlConfigSnapshot(nil)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	outcome, err := projectsetup.StartOrResume(r.Context(), queries, projectID, user.ID, func(ctx context.Context) (pgtype.UUID, error) {
		crawl, err := insertQueuedCrawl(ctx, queries, projectID, user.ID, projectSetupCrawlSource, normalizedConfigSnapshot)
		if err != nil {
			return pgtype.UUID{}, err
		}
		return crawl.ID, nil
	})
	if err != nil {
		if !writeProjectSetupStartError(w, err) {
			serverError(w, r, err)
		}
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	status := http.StatusOK
	if outcome.Started {
		status = http.StatusCreated
	}
	setNoStore(w)
	writeJSON(w, status, newProjectSetupResponse(outcome.Setup))
}

// writeProjectSetupStartError maps a start or resume failure to its response. It
// returns false for unexpected errors so the caller logs an opaque 500.
func writeProjectSetupStartError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, projectsetup.ErrSetupNotFound):
		writeJSONError(w, http.StatusNotFound, "project setup not found")
	case errors.Is(err, projectsetup.ErrInvalidFailedStep), errors.Is(err, projectsetup.ErrResumeConflict):
		writeJSONError(w, http.StatusConflict, "project setup cannot be resumed")
	default:
		return false
	}
	return true
}

// newProjectSetupResponse converts a setup row into an API response.
func newProjectSetupResponse(setup sqlc.ProjectSetup) projectSetupResponse {
	response := projectSetupResponse{
		ID:                   setup.ID.String(),
		OrganizationID:       setup.OrganizationID.String(),
		ProjectID:            setup.ProjectID.String(),
		RequestedByUserID:    setup.RequestedByUserID.String(),
		Status:               setup.Status,
		Error:                textValue(setup.Error),
		FailedStep:           textValue(setup.FailedStep),
		VisibilitySkipReason: textValue(setup.VisibilitySkipReason),
		CreatedAt:            formatTimestamp(setup.CreatedAt),
		UpdatedAt:            formatTimestamp(setup.UpdatedAt),
	}
	if setup.CrawlID.Valid {
		response.CrawlID = setup.CrawlID.String()
	}
	if setup.CompletedAt.Valid {
		response.CompletedAt = formatTimestamp(setup.CompletedAt)
	}
	return response
}
