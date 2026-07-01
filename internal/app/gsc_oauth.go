package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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
	defer func() { _ = tx.Rollback(r.Context()) }()

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

	// Consume the state token immediately so it cannot be replayed: a failed or
	// zero-row delete means the state was already used (or is otherwise gone) and
	// the callback must abort before any code exchange happens.
	deletedRows, err := a.Queries.DeleteGoogleOAuthStateByID(r.Context(), oauthState.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if deletedRows == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid oauth state")
		return
	}

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
	defer func() { _ = tx.Rollback(r.Context()) }()

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
	returnURL, err := url.Parse(normalizeGoogleOAuthReturnPath(oauthState.ReturnPath))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	frontendURL.Path = joinURLPath(frontendURL.Path, returnURL.Path)

	query := frontendURL.Query()
	for key, values := range returnURL.Query() {
		for _, value := range values {
			query.Add(key, value)
		}
	}
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
