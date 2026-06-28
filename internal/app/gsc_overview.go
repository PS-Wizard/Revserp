package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// handleProjectGSCOverview fetches one project's Search Console overview (cached up to 1 hour).
func (a *App) handleProjectGSCOverview(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	// All DB work happens on a.Queries directly — no transaction held across Google API calls.
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(r.Context(), a.Queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !hasProjectConnection {
		writeJSONError(w, http.StatusBadRequest, "project is not connected to google search console")
		return
	}

	googleConnection, hasGoogleConnection, err := getGoogleConnectionByOrganizationID(r.Context(), a.Queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !hasGoogleConnection {
		writeJSONError(w, http.StatusBadRequest, "google search console is not connected")
		return
	}

	// ensureFreshGoogleConnection may write to DB (token refresh); runs on pool directly, not inside a tx.
	googleConnection, accessToken, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, googleConnection)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return
	}

	// Google API call is outside any DB transaction and uses the in-process TTL cache.
	overview, err := a.GSCService.FetchOverviewCached(r.Context(), accessToken, projectConnection.SiteUrl)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console data")
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
