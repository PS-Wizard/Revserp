package app

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// handleProjectGSCQueries returns one page of Search Console query rows.
// Unlike the overview's fixed top-25 slice, search and question filtering run at
// Google, so paging walks the whole matching set.
func (a *App) handleProjectGSCQueries(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	// All DB work happens on a.Queries directly — no transaction held across Google API calls.
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
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

	// Bounds are enforced by QueryPageOptions.normalized; parsing only needs to
	// turn absent or malformed values into the zero that selects the default.
	params := r.URL.Query()
	options := gsc.QueryPageOptions{
		Days:          parseIntQueryParam(params.Get("days")),
		Limit:         parseIntQueryParam(params.Get("limit")),
		Offset:        parseIntQueryParam(params.Get("offset")),
		Search:        params.Get("search"),
		Dimension:     params.Get("dimension"),
		QuestionsOnly: params.Get("preset") == "questions",
	}

	page, err := a.GSCService.FetchQueriesCached(r.Context(), accessToken, project.OrganizationID.String(), projectConnection.SiteUrl, options)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console queries")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project_id":        project.ID.String(),
		"site_url":          projectConnection.SiteUrl,
		"permission_level":  textValue(projectConnection.PermissionLevel),
		"google_connection": googleConnection.ID.String(),
		"queries":           page,
	})
}

func parseIntQueryParam(raw string) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}
