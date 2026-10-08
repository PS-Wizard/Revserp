package app

import (
	"context"
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
// Mode "" (legacy connect) matches the verified account or creates a new one;
// "add_account" always stores a distinct account; "reconnect_account" targets one
// google_connection_id and rejects a different verified account.
func (a *App) handleStartProjectGSCConnect(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody startProjectGSCConnectRequest
	if err := decodeOptionalJSON(r, &requestBody); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
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
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	purpose := strings.TrimSpace(requestBody.Mode)
	if purpose == "" {
		purpose = "connect"
	}
	if purpose != "connect" && purpose != "add_account" && purpose != "reconnect_account" {
		writeJSONError(w, http.StatusBadRequest, "invalid mode")
		return
	}
	var targetConnectionID pgtype.UUID
	if purpose == "reconnect_account" {
		targetConnectionID, err = parseUUIDParam(strings.TrimSpace(requestBody.GoogleConnectionID))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid google connection id")
			return
		}
		targetConnection, found, err := getGoogleAccountConnectionByID(r.Context(), tx, targetConnectionID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if !found || !uuidEqual(targetConnection.OrganizationID, project.OrganizationID) {
			writeJSONError(w, http.StatusNotFound, "google account not found")
			return
		}
	}

	stateToken, err := generateGoogleOAuthStateToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	_, err = createGoogleOAuthStateWithPurpose(
		r.Context(), tx,
		hashGoogleOAuthStateToken(stateToken),
		project.OrganizationID, user.ID, project.ID,
		normalizeGoogleOAuthReturnPath(requestBody.ReturnPath),
		purpose, targetConnectionID,
		timestamptzValue(time.Now().UTC().Add(googleOAuthStateTTL)),
	)
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
// The verified Google subject decides which account row is written: a reconnect
// targets one account and rejects a different subject, otherwise the matching
// account is updated or a new account row is created. Tokens are never copied
// across accounts.
func (a *App) handleGoogleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	stateToken := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	oauthError := strings.TrimSpace(r.URL.Query().Get("error"))
	if stateToken == "" {
		writeJSONError(w, http.StatusBadRequest, "missing oauth state")
		return
	}

	oauthState, found, err := getGoogleOAuthStateWithPurpose(r.Context(), a.DB, hashGoogleOAuthStateToken(stateToken))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !found {
		writeJSONError(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	legacyState := sqlc.GoogleOauthState{
		ID:             oauthState.ID,
		StateTokenHash: oauthState.StateTokenHash,
		OrganizationID: oauthState.OrganizationID,
		UserID:         oauthState.UserID,
		ProjectID:      oauthState.ProjectID,
		ReturnPath:     oauthState.ReturnPath,
		ExpiresAt:      oauthState.ExpiresAt,
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
		a.writeGoogleOAuthRedirect(w, r, legacyState, "oauth_state_expired")
		return
	}
	if oauthError != "" {
		a.writeGoogleOAuthRedirect(w, r, legacyState, oauthError)
		return
	}
	if code == "" {
		a.writeGoogleOAuthRedirect(w, r, legacyState, "missing_oauth_code")
		return
	}

	tokenResponse, err := a.GSCService.ExchangeCode(r.Context(), code)
	if err != nil {
		a.writeGoogleOAuthRedirect(w, r, legacyState, errorToCallbackCode(err))
		return
	}

	verifiedIdentity, err := a.GSCService.FetchVerifiedGoogleIdentity(r.Context(), tokenResponse.AccessToken)
	if err != nil {
		a.writeGoogleOAuthRedirect(w, r, legacyState, "google_identity_verification_failed")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	if err := a.storeVerifiedGoogleConnection(r.Context(), queries, tx, oauthState, tokenResponse, verifiedIdentity); err != nil {
		var callbackErr *googleOAuthCallbackError
		if errors.As(err, &callbackErr) {
			a.writeGoogleOAuthRedirect(w, r, legacyState, callbackErr.Code)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	a.writeGoogleOAuthRedirect(w, r, legacyState, "")
}

type googleOAuthCallbackError struct {
	Code string
}

func (err *googleOAuthCallbackError) Error() string {
	return "google oauth callback: " + err.Code
}

func (a *App) storeVerifiedGoogleConnection(ctx context.Context, queries *sqlc.Queries, tx googleAccountDB, oauthState googleOAuthStateWithPurpose, tokenResponse gsc.TokenResponse, verified gsc.GoogleVerifiedIdentity) error {
	if oauthState.GoogleConnectionID.Valid {
		return a.storeGoogleReconnect(ctx, queries, tx, oauthState, tokenResponse, verified)
	}
	return a.storeGoogleMatchOrCreate(ctx, queries, tx, oauthState, tokenResponse, verified)
}

// storeGoogleReconnect writes fresh tokens to one targeted account row only, after
// proving the verified identity equals the account's established identity. A
// legacy row without a stored subject is verified through its stored refresh
// token; when that proof is unavailable the reconnect fails closed so a
// different Google user can never silently take over the row's bindings.
func (a *App) storeGoogleReconnect(ctx context.Context, queries *sqlc.Queries, tx googleAccountDB, oauthState googleOAuthStateWithPurpose, tokenResponse gsc.TokenResponse, verified gsc.GoogleVerifiedIdentity) error {
	existing, found, err := getGoogleAccountConnectionByID(ctx, tx, oauthState.GoogleConnectionID)
	if err != nil {
		return err
	}
	if !found || !uuidEqual(existing.OrganizationID, oauthState.OrganizationID) {
		return &googleOAuthCallbackError{Code: "invalid_google_account"}
	}
	establishedSubject, err := a.establishedGoogleAccountSubject(ctx, existing)
	if err != nil {
		return &googleOAuthCallbackError{Code: "google_account_mismatch"}
	}
	if err := validateReconnectSubject(establishedSubject, verified.Subject); err != nil {
		return &googleOAuthCallbackError{Code: "google_account_mismatch"}
	}
	if strings.TrimSpace(textValue(existing.GoogleAccountSubject)) == "" {
		if err := adoptGoogleAccountConnectionIdentity(ctx, tx, existing.ID, verified.Email, verified.Subject); err != nil {
			return err
		}
	}
	return a.updateGoogleAccountTokens(ctx, queries, existing, tokenResponse)
}

// establishedGoogleAccountSubject returns the Google-verified subject for one
// stored account: the recorded subject when present, otherwise the subject
// proven by refreshing the stored refresh token. It errors when neither
// proof exists, so callers fail closed.
func (a *App) establishedGoogleAccountSubject(ctx context.Context, existing sqlc.GoogleConnection) (string, error) {
	if subject := strings.TrimSpace(textValue(existing.GoogleAccountSubject)); subject != "" {
		return subject, nil
	}
	refreshToken, err := a.GSCService.DecryptSecret(existing.EncryptedRefreshToken)
	if err != nil || strings.TrimSpace(refreshToken) == "" {
		return "", err
	}
	refreshedToken, err := a.GSCService.RefreshAccessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	establishedIdentity, err := a.GSCService.FetchVerifiedGoogleIdentity(ctx, refreshedToken.AccessToken)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(establishedIdentity.Subject) == "" {
		return "", errGoogleAccountMismatch
	}
	return strings.TrimSpace(establishedIdentity.Subject), nil
}

// storeGoogleMatchOrCreate updates the account whose stored subject equals the
// verified subject, or creates a new canonical account row. Legacy
// subject-less rows are never adopted here: they keep their IDs and bindings
// until the owner explicitly reconnects or reselects.
func (a *App) storeGoogleMatchOrCreate(ctx context.Context, queries *sqlc.Queries, tx googleAccountDB, oauthState googleOAuthStateWithPurpose, tokenResponse gsc.TokenResponse, verified gsc.GoogleVerifiedIdentity) error {
	matched, found, err := getGoogleAccountConnectionByOrganizationSubject(ctx, tx, oauthState.OrganizationID, verified.Subject)
	if err != nil {
		return err
	}
	if found {
		return a.updateGoogleAccountTokens(ctx, queries, matched, tokenResponse)
	}

	if strings.TrimSpace(tokenResponse.RefreshToken) == "" {
		return &googleOAuthCallbackError{Code: "missing_refresh_token"}
	}
	encryptedAccessToken, err := a.GSCService.EncryptSecret(tokenResponse.AccessToken)
	if err != nil {
		return err
	}
	encryptedRefreshToken, err := a.GSCService.EncryptSecret(tokenResponse.RefreshToken)
	if err != nil {
		return err
	}
	_, err = createGoogleAccountConnection(ctx, tx, createGoogleAccountConnectionParams{
		OrganizationID:        oauthState.OrganizationID,
		ConnectedByUserID:     oauthState.UserID,
		GoogleAccountEmail:    verified.Email,
		GoogleAccountSubject:  verified.Subject,
		EncryptedRefreshToken: encryptedRefreshToken,
		EncryptedAccessToken:  encryptedAccessToken,
		AccessTokenExpiresAt:  timestamptzValue(computeGoogleTokenExpiry(tokenResponse.ExpiresIn)),
		Scope:                 defaultGoogleScope(tokenResponse.Scope),
	})
	return err
}

// updateGoogleAccountTokens writes fresh tokens to one account row only, never
// across accounts. A same-account update keeps the stored refresh token when
// Google omits a new one; a row with no refresh token at all fails closed.
func (a *App) updateGoogleAccountTokens(ctx context.Context, queries *sqlc.Queries, existing sqlc.GoogleConnection, tokenResponse gsc.TokenResponse) error {
	encryptedAccessToken, err := a.GSCService.EncryptSecret(tokenResponse.AccessToken)
	if err != nil {
		return err
	}

	encryptedRefreshToken := existing.EncryptedRefreshToken
	if strings.TrimSpace(tokenResponse.RefreshToken) != "" {
		encryptedRefreshToken, err = a.GSCService.EncryptSecret(tokenResponse.RefreshToken)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(encryptedRefreshToken) == "" {
		return &googleOAuthCallbackError{Code: "missing_refresh_token"}
	}

	scope := strings.TrimSpace(tokenResponse.Scope)
	if scope == "" {
		scope = existing.Scope
	}
	if scope == "" {
		scope = defaultGoogleScope("")
	}

	_, err = queries.UpdateGoogleConnectionTokens(ctx, sqlc.UpdateGoogleConnectionTokensParams{
		ID:                    existing.ID,
		EncryptedAccessToken:  pgText(encryptedAccessToken),
		EncryptedRefreshToken: encryptedRefreshToken,
		AccessTokenExpiresAt:  timestamptzValue(computeGoogleTokenExpiry(tokenResponse.ExpiresIn)),
		Scope:                 scope,
		Status:                "active",
		LastError:             pgtype.Text{},
	})
	return err
}

func defaultGoogleScope(scope string) string {
	if strings.TrimSpace(scope) != "" {
		return strings.TrimSpace(scope)
	}
	return "https://www.googleapis.com/auth/webmasters.readonly " + googleAnalyticsReadOnlyScope
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
	frontendURL.Fragment = returnURL.Fragment

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
