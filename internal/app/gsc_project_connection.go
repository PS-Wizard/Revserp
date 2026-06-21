package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// handleProjectGSCStatus returns one project's current Google connection and property selection state.
func (a *App) handleProjectGSCStatus(w http.ResponseWriter, r *http.Request) {
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
	defer tx.Rollback(r.Context())

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

	membership, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	googleConnection, hasGoogleConnection, err := getGoogleConnectionByOrganizationID(r.Context(), queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(r.Context(), queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response := projectGSCStatusResponse{
		HasGoogleConnection: hasGoogleConnection,
		CanManageConnection: membership.Role == "owner",
		AvailableSites:      []projectGSCSiteResponse{},
	}
	if !hasGoogleConnection {
		if err := tx.Commit(r.Context()); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	response.GoogleConnectionID = googleConnection.ID.String()
	response.GoogleStatus = googleConnection.Status
	response.GoogleAccountEmail = textValue(googleConnection.GoogleAccountEmail)
	response.NeedsReconnect = googleConnection.Status == "reauth_required"

	if hasProjectConnection {
		response.SelectedSite = &projectGSCSiteResponse{
			SiteURL:         projectConnection.SiteUrl,
			PermissionLevel: textValue(projectConnection.PermissionLevel),
		}
		response.Connected = true
	}

	if googleConnection.Status == "active" {
		googleConnection, accessToken, refreshErr := a.ensureFreshGoogleConnection(r.Context(), queries, googleConnection)
		if refreshErr != nil {
			response.GoogleStatus = googleConnection.Status
			response.NeedsReconnect = googleConnection.Status == "reauth_required"
			response.TokenError = refreshErr.Error()
		} else {
			sites, fetchErr := a.GSCService.FetchSites(r.Context(), accessToken)
			if fetchErr != nil {
				response.TokenError = fetchErr.Error()
			} else {
				rankedSites := a.GSCService.RankSitesForProject(project.BaseUrl, sites)
				response.AvailableSites = newProjectGSCSiteResponses(rankedSites)
			}
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleSelectProjectGSCSite stores one owner-selected Search Console property for a project.
func (a *App) handleSelectProjectGSCSite(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody selectProjectGSCSiteRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	siteURL := strings.TrimSpace(requestBody.SiteURL)
	if siteURL == "" {
		writeJSONError(w, http.StatusBadRequest, "site_url is required")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

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
	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
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

	sites, err := a.GSCService.FetchSites(r.Context(), accessToken)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console sites")
		return
	}

	var selectedSite *gsc.SiteEntry
	for siteIndex := range sites {
		if sites[siteIndex].SiteURL == siteURL {
			selectedSite = &sites[siteIndex]
			break
		}
	}
	if selectedSite == nil {
		writeJSONError(w, http.StatusBadRequest, "selected property is not available for this google account")
		return
	}

	_, err = queries.UpsertProjectGSCConnection(r.Context(), sqlc.UpsertProjectGSCConnectionParams{
		ProjectID:          project.ID,
		GoogleConnectionID: googleConnection.ID,
		SiteUrl:            selectedSite.SiteURL,
		PermissionLevel:    pgText(selectedSite.PermissionLevel),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDisconnectProjectGSC removes one project-level Search Console property selection.
func (a *App) handleDisconnectProjectGSC(w http.ResponseWriter, r *http.Request) {
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
	defer tx.Rollback(r.Context())

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
	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	deletedRows, err := queries.DeleteProjectGSCConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if deletedRows == 0 {
		writeJSONError(w, http.StatusNotFound, "project is not connected to google search console")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func getGoogleConnectionByOrganizationID(ctx context.Context, queries *sqlc.Queries, organizationID pgtype.UUID) (sqlc.GoogleConnection, bool, error) {
	connection, err := queries.GetGoogleConnectionByOrganizationID(ctx, organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GoogleConnection{}, false, nil
		}
		return sqlc.GoogleConnection{}, false, err
	}
	return connection, true, nil
}

func getProjectGSCConnectionByProjectID(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (sqlc.ProjectGscConnection, bool, error) {
	connection, err := queries.GetProjectGSCConnectionByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.ProjectGscConnection{}, false, nil
		}
		return sqlc.ProjectGscConnection{}, false, err
	}
	return connection, true, nil
}

func newProjectGSCSiteResponses(sites []gsc.SiteEntry) []projectGSCSiteResponse {
	responses := make([]projectGSCSiteResponse, 0, len(sites))
	for _, site := range sites {
		responses = append(responses, projectGSCSiteResponse{
			SiteURL:         site.SiteURL,
			PermissionLevel: site.PermissionLevel,
			MatchScore:      site.MatchScore,
		})
	}
	return responses
}

func writeGoogleAPIError(w http.ResponseWriter, err error, fallbackStatusCode int, fallbackMessage string) {
	var googleError *gsc.Error
	if errors.As(err, &googleError) {
		writeJSONError(w, fallbackStatusCode, googleError.Message)
		return
	}
	writeJSONError(w, fallbackStatusCode, fallbackMessage)
}
