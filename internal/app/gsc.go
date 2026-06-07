package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

const googleOAuthStateTTL = 15 * time.Minute

type startProjectGSCConnectRequest struct {
	ReturnPath string `json:"return_path"`
}

type selectProjectGSCSiteRequest struct {
	SiteURL string `json:"site_url"`
}

type projectGSCStatusResponse struct {
	HasGoogleConnection bool                     `json:"has_google_connection"`
	GoogleConnectionID  string                   `json:"google_connection_id,omitempty"`
	GoogleAccountEmail  string                   `json:"google_account_email,omitempty"`
	GoogleStatus        string                   `json:"google_status,omitempty"`
	NeedsReconnect      bool                     `json:"needs_reconnect"`
	CanManageConnection bool                     `json:"can_manage_connection"`
	Connected           bool                     `json:"connected"`
	SelectedSite        *projectGSCSiteResponse  `json:"selected_site,omitempty"`
	AvailableSites      []projectGSCSiteResponse `json:"available_sites"`
	TokenError          string                   `json:"token_error,omitempty"`
}

type projectGSCSiteResponse struct {
	SiteURL         string `json:"site_url"`
	PermissionLevel string `json:"permission_level,omitempty"`
	MatchScore      int    `json:"match_score,omitempty"`
}

// handleStartProjectGSCConnect creates one Google OAuth consent URL for an owner-managed project.
func (a *App) handleStartProjectGSCConnect(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody startProjectGSCConnectRequest
	if err := decodeOptionalJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
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

	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
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

	stateToken, err := generateGoogleOAuthStateToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	_, err = queries.CreateGoogleOAuthState(r.Context(), sqlc.CreateGoogleOAuthStateParams{
		StateTokenHash: hashGoogleOAuthStateToken(stateToken),
		OrganizationID: project.OrganizationID,
		UserID:         user.ID,
		ProjectID:      project.ID,
		ReturnPath:     normalizeGoogleOAuthReturnPath(requestBody.ReturnPath),
		ExpiresAt:      timestamptzValue(time.Now().UTC().Add(googleOAuthStateTTL)),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	authURL, err := a.GSCService.BuildAuthURL(stateToken)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

// handleGoogleOAuthCallback exchanges one Google callback code for stored organization credentials.
func (a *App) handleGoogleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	stateToken := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	oauthError := strings.TrimSpace(r.URL.Query().Get("error"))
	if stateToken == "" {
		writeJSONError(w, http.StatusBadRequest, "missing oauth state")
		return
	}

	oauthState, err := a.Queries.GetGoogleOAuthStateByTokenHash(r.Context(), hashGoogleOAuthStateToken(stateToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "invalid oauth state")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer a.Queries.DeleteGoogleOAuthStateByID(r.Context(), oauthState.ID)

	if oauthState.ExpiresAt.Valid && time.Now().UTC().After(oauthState.ExpiresAt.Time) {
		a.writeGoogleOAuthRedirect(w, r, oauthState, "oauth_state_expired")
		return
	}
	if oauthError != "" {
		a.writeGoogleOAuthRedirect(w, r, oauthState, oauthError)
		return
	}
	if code == "" {
		a.writeGoogleOAuthRedirect(w, r, oauthState, "missing_oauth_code")
		return
	}

	tokenResponse, err := a.GSCService.ExchangeCode(r.Context(), code)
	if err != nil {
		a.writeGoogleOAuthRedirect(w, r, oauthState, errorToCallbackCode(err))
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	existingConnection, existingConnectionFound, err := getGoogleConnectionByOrganizationID(r.Context(), queries, oauthState.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	encryptedAccessToken, err := a.GSCService.EncryptSecret(tokenResponse.AccessToken)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	encryptedRefreshToken := ""
	if existingConnectionFound {
		encryptedRefreshToken = existingConnection.EncryptedRefreshToken
	}
	if strings.TrimSpace(tokenResponse.RefreshToken) != "" {
		encryptedRefreshToken, err = a.GSCService.EncryptSecret(tokenResponse.RefreshToken)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	if encryptedRefreshToken == "" {
		a.writeGoogleOAuthRedirect(w, r, oauthState, "missing_refresh_token")
		return
	}

	scope := strings.TrimSpace(tokenResponse.Scope)
	if scope == "" && existingConnectionFound {
		scope = existingConnection.Scope
	}

	_, err = queries.UpsertGoogleConnectionForOrganization(r.Context(), sqlc.UpsertGoogleConnectionForOrganizationParams{
		OrganizationID:        oauthState.OrganizationID,
		ConnectedByUserID:     oauthState.UserID,
		GoogleAccountEmail:    pgtype.Text{},
		GoogleAccountSubject:  pgtype.Text{},
		EncryptedRefreshToken: encryptedRefreshToken,
		EncryptedAccessToken:  pgText(encryptedAccessToken),
		AccessTokenExpiresAt:  timestamptzValue(computeGoogleTokenExpiry(tokenResponse.ExpiresIn)),
		Scope:                 scope,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	a.writeGoogleOAuthRedirect(w, r, oauthState, "")
}

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

func (a *App) ensureFreshGoogleConnection(ctx context.Context, queries *sqlc.Queries, connection sqlc.GoogleConnection) (sqlc.GoogleConnection, string, error) {
	if connection.Status != "active" {
		return connection, "", &gsc.Error{Message: "google connection requires reconnect"}
	}

	accessToken, err := a.GSCService.DecryptSecret(textValue(connection.EncryptedAccessToken))
	if err != nil {
		return connection, "", err
	}
	if accessToken != "" && connection.AccessTokenExpiresAt.Valid && connection.AccessTokenExpiresAt.Time.UTC().After(time.Now().UTC().Add(time.Minute)) {
		return connection, accessToken, nil
	}

	refreshToken, err := a.GSCService.DecryptSecret(connection.EncryptedRefreshToken)
	if err != nil {
		return connection, "", err
	}
	refreshedToken, err := a.GSCService.RefreshAccessToken(ctx, refreshToken)
	if err != nil {
		markErr := queries.UpdateGoogleConnectionStatus(ctx, sqlc.UpdateGoogleConnectionStatusParams{
			ID:        connection.ID,
			Status:    "reauth_required",
			LastError: pgText(err.Error()),
		})
		if markErr == nil {
			connection.Status = "reauth_required"
			connection.LastError = pgText(err.Error())
		}
		return connection, "", err
	}

	encryptedAccessToken, err := a.GSCService.EncryptSecret(refreshedToken.AccessToken)
	if err != nil {
		return connection, "", err
	}
	encryptedRefreshToken := connection.EncryptedRefreshToken
	if strings.TrimSpace(refreshedToken.RefreshToken) != "" {
		encryptedRefreshToken, err = a.GSCService.EncryptSecret(refreshedToken.RefreshToken)
		if err != nil {
			return connection, "", err
		}
	}
	updatedConnection, err := queries.UpdateGoogleConnectionTokens(ctx, sqlc.UpdateGoogleConnectionTokensParams{
		ID:                    connection.ID,
		EncryptedAccessToken:  pgText(encryptedAccessToken),
		EncryptedRefreshToken: encryptedRefreshToken,
		AccessTokenExpiresAt:  timestamptzValue(computeGoogleTokenExpiry(refreshedToken.ExpiresIn)),
		Scope:                 coalesceString(refreshedToken.Scope, connection.Scope),
		Status:                "active",
		LastError:             pgtype.Text{},
	})
	if err != nil {
		return connection, "", err
	}
	return updatedConnection, refreshedToken.AccessToken, nil
}

func decodeOptionalJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	if r.ContentLength == 0 {
		return nil
	}
	err := readJSON(r, target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
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

func (a *App) writeGoogleOAuthRedirect(w http.ResponseWriter, r *http.Request, oauthState sqlc.GoogleOauthState, callbackErrorCode string) {
	if strings.TrimSpace(a.Config.FrontendURL) == "" {
		payload := map[string]any{
			"ok":         callbackErrorCode == "",
			"project_id": oauthState.ProjectID.String(),
		}
		if callbackErrorCode != "" {
			payload["error"] = callbackErrorCode
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	frontendURL, err := url.Parse(strings.TrimRight(a.Config.FrontendURL, "/"))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	frontendURL.Path = joinURLPath(frontendURL.Path, oauthState.ReturnPath)

	query := frontendURL.Query()
	query.Set("gsc_project_id", oauthState.ProjectID.String())
	if callbackErrorCode == "" {
		query.Set("gsc_status", "connected")
	} else {
		query.Set("gsc_status", "error")
		query.Set("gsc_error", callbackErrorCode)
	}
	frontendURL.RawQuery = query.Encode()
	http.Redirect(w, r, frontendURL.String(), http.StatusFound)
}

func writeGoogleAPIError(w http.ResponseWriter, err error, fallbackStatusCode int, fallbackMessage string) {
	var googleError *gsc.Error
	if errors.As(err, &googleError) {
		writeJSONError(w, fallbackStatusCode, googleError.Message)
		return
	}
	writeJSONError(w, fallbackStatusCode, fallbackMessage)
}

func generateGoogleOAuthStateToken() (string, error) {
	rawTokenBytes := make([]byte, 32)
	if _, err := rand.Read(rawTokenBytes); err != nil {
		return "", fmt.Errorf("generate google oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(rawTokenBytes), nil
}

func hashGoogleOAuthStateToken(stateToken string) string {
	tokenHash := sha256.Sum256([]byte(stateToken))
	return hex.EncodeToString(tokenHash[:])
}

func normalizeGoogleOAuthReturnPath(returnPath string) string {
	trimmedPath := strings.TrimSpace(returnPath)
	if trimmedPath == "" || !strings.HasPrefix(trimmedPath, "/") {
		return "/"
	}
	return trimmedPath
}

func computeGoogleTokenExpiry(expiresInSeconds int) time.Time {
	expiresIn := expiresInSeconds
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	refreshSkewSeconds := expiresIn - 60
	if refreshSkewSeconds < 0 {
		refreshSkewSeconds = 0
	}
	return time.Now().UTC().Add(time.Duration(refreshSkewSeconds) * time.Second)
}

func coalesceString(primaryValue, fallbackValue string) string {
	if strings.TrimSpace(primaryValue) != "" {
		return primaryValue
	}
	return fallbackValue
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func errorToCallbackCode(err error) string {
	var googleError *gsc.Error
	if errors.As(err, &googleError) {
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(googleError.Message)), " ", "_")
	}
	return "google_oauth_failed"
}

func joinURLPath(basePath, returnPath string) string {
	trimmedBasePath := strings.TrimRight(strings.TrimSpace(basePath), "/")
	trimmedReturnPath := normalizeGoogleOAuthReturnPath(returnPath)
	if trimmedBasePath == "" {
		return trimmedReturnPath
	}
	if trimmedReturnPath == "/" {
		return trimmedBasePath
	}
	return trimmedBasePath + trimmedReturnPath
}

func timestamptzValue(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
