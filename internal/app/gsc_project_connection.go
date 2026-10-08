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
	"github.com/ps-wizard/revserp/internal/ga"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// handleProjectGSCStatus returns one project's current Google connection and property selection state.
// With several org accounts the bound account backs the selection; without a
// binding only a single-account org resolves implicitly.
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
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
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

	accounts, err := listGoogleAccountConnectionsByOrganizationID(r.Context(), tx, project.OrganizationID)
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
		HasGoogleConnection: len(accounts) > 0,
		CanManageConnection: membership.Role == "owner",
		AvailableSites:      []projectGSCSiteResponse{},
		GoogleConnections:   newProjectGoogleAccountResponses(accounts),
	}
	if len(accounts) == 0 {
		if err := tx.Commit(r.Context()); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	var boundConnectionID *pgtype.UUID
	if hasProjectConnection {
		boundConnectionID = &projectConnection.GoogleConnectionID
		response.SelectedSite = &projectGSCSiteResponse{
			SiteURL:         projectConnection.SiteUrl,
			PermissionLevel: textValue(projectConnection.PermissionLevel),
		}
		response.Connected = true
		response.SelectedGoogleConnectionID = projectConnection.GoogleConnectionID.String()
	}

	displayConnection, err := matchGoogleAccountForSelect(accounts, boundConnectionID, nil)
	if err != nil {
		if errors.Is(err, errGoogleConnectionRequired) || errors.Is(err, errGoogleAccountNotFound) {
			if errors.Is(err, errGoogleAccountNotFound) {
				response.TokenError = "google account not found"
			}
			if err := tx.Commit(r.Context()); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			writeJSON(w, http.StatusOK, response)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response.GoogleConnectionID = displayConnection.ID.String()
	response.GoogleStatus = displayConnection.Status
	response.GoogleAccountEmail = textValue(displayConnection.GoogleAccountEmail)
	response.NeedsReconnect = displayConnection.Status == "reauth_required"

	if displayConnection.Status == "active" {
		displayConnection, accessToken, refreshErr := a.ensureFreshGoogleConnection(r.Context(), queries, displayConnection)
		if refreshErr != nil {
			response.GoogleStatus = displayConnection.Status
			response.NeedsReconnect = displayConnection.Status == "reauth_required"
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
// The property is validated against the explicitly chosen or already-bound
// account, never an arbitrary organization-first account.
func (a *App) handleSelectProjectGSCSite(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody selectProjectGSCSiteRequest
	if !readJSONOrRespond(w, r, &requestBody) {
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
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
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

	accounts, err := listGoogleAccountConnectionsByOrganizationID(r.Context(), tx, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if len(accounts) == 0 {
		writeJSONError(w, http.StatusBadRequest, "google search console is not connected")
		return
	}

	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(r.Context(), queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	var boundConnectionID *pgtype.UUID
	if hasProjectConnection {
		boundConnectionID = &projectConnection.GoogleConnectionID
	}
	var requestedConnectionID *pgtype.UUID
	if strings.TrimSpace(requestBody.GoogleConnectionID) != "" {
		parsedConnectionID, err := parseUUIDParam(strings.TrimSpace(requestBody.GoogleConnectionID))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid google connection id")
			return
		}
		requestedConnectionID = &parsedConnectionID
	}
	googleConnection, err := matchGoogleAccountForSelect(accounts, boundConnectionID, requestedConnectionID)
	if err != nil {
		if errors.Is(err, errGoogleConnectionRequired) {
			writeJSONError(w, http.StatusBadRequest, "google_connection_id is required")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "google account not found")
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
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
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

// handleListProjectGoogleAccounts lists every org-owned Google account for one project.
func (a *App) handleListProjectGoogleAccounts(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	membership, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	accounts, err := listGoogleAccountConnectionsByOrganizationID(r.Context(), a.DB, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"google_connections":    newProjectGoogleAccountResponses(accounts),
		"can_manage_connection": membership.Role == "owner",
	})
}

// handleRevokeProjectGoogleAccount marks one org-owned Google account revoked without
// deleting the row: project and location bindings plus stored reports are preserved.
func (a *App) handleRevokeProjectGoogleAccount(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	connectionID, err := parseUUIDParam(chi.URLParam(r, "connectionID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid google connection id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	account, found, err := getGoogleAccountConnectionByID(r.Context(), a.DB, connectionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !found || !uuidEqual(account.OrganizationID, project.OrganizationID) {
		writeJSONError(w, http.StatusNotFound, "google account not found")
		return
	}
	if err := revokeGoogleAccountConnection(r.Context(), a.DB, connectionID, "revoked by organization owner"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func newProjectGoogleAccountResponses(accounts []sqlc.GoogleConnection) []projectGoogleAccountResponse {
	responses := make([]projectGoogleAccountResponse, 0, len(accounts))
	for _, account := range accounts {
		responses = append(responses, projectGoogleAccountResponse{
			ID:                 account.ID.String(),
			GoogleAccountEmail: textValue(account.GoogleAccountEmail),
			GoogleStatus:       account.Status,
		})
	}
	return responses
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

// googleAccountForProject loads one org-owned Google account in project scope.
// Any project member may read; writes enforce ownership at the handler.
func (a *App) googleAccountForProject(w http.ResponseWriter, r *http.Request) (sqlc.GoogleConnection, sqlc.Project, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	connectionID, err := parseUUIDParam(chi.URLParam(r, "connectionID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid google connection id")
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return sqlc.GoogleConnection{}, sqlc.Project{}, false
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	if _, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: principal.User.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return sqlc.GoogleConnection{}, sqlc.Project{}, false
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	account, found, err := getGoogleAccountConnectionByID(r.Context(), a.DB, connectionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	if !found || !uuidEqual(account.OrganizationID, project.OrganizationID) {
		writeJSONError(w, http.StatusNotFound, "google account not found")
		return sqlc.GoogleConnection{}, sqlc.Project{}, false
	}
	return account, project, true
}

// handleListGoogleAccountGSCSites lists Search Console properties for one explicitly
// chosen account, so account/property pickers work before anything is bound.
func (a *App) handleListGoogleAccountGSCSites(w http.ResponseWriter, r *http.Request) {
	account, project, ok := a.googleAccountForProject(w, r)
	if !ok {
		return
	}
	account, accessToken, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, account)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return
	}
	sites, err := a.GSCService.FetchSites(r.Context(), accessToken)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console sites")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"google_connection_id": account.ID.String(),
		"google_account_email": textValue(account.GoogleAccountEmail),
		"google_status":        account.Status,
		"available_sites":      newProjectGSCSiteResponses(a.GSCService.RankSitesForProject(project.BaseUrl, sites)),
	})
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
	var analyticsError *ga.Error
	if errors.As(err, &analyticsError) {
		writeJSONError(w, fallbackStatusCode, analyticsError.Message)
		return
	}
	writeJSONError(w, fallbackStatusCode, fallbackMessage)
}
