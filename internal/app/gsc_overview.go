package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// handleProjectGSCOverview fetches one project's Search Console overview live from Google.
func (a *App) handleProjectGSCOverview(w http.ResponseWriter, r *http.Request) {
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
	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(r.Context(), queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !hasProjectConnection {
		writeJSONError(w, http.StatusBadRequest, "project is not connected to google search console")
		return
	}

	googleConnection, hasGoogleConnection, err := getGoogleConnectionByOrganizationID(r.Context(), queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !hasGoogleConnection {
		writeJSONError(w, http.StatusBadRequest, "google search console is not connected")
		return
	}

	googleConnection, accessToken, err := a.ensureFreshGoogleConnection(r.Context(), queries, googleConnection)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return
	}

	overview, err := a.GSCService.FetchOverview(r.Context(), accessToken, projectConnection.SiteUrl)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console data")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id":        project.ID.String(),
		"site_url":          projectConnection.SiteUrl,
		"permission_level":  textValue(projectConnection.PermissionLevel),
		"google_connection": googleConnection.ID.String(),
		"overview":          overview,
	})
}
