package app

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

type authCredentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type signUpPendingResponse struct {
	SignupCompletedWithoutSession bool   `json:"signup_completed_without_session"`
	Email                         string `json:"email"`
}

type authOAuthExchangeRequest struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
}

type sessionRenewalResponse struct {
	Renewed    bool   `json:"renewed"`
	ExpiresAt  string `json:"expires_at"`
	RenewAfter string `json:"renew_after"`
	RetryAfter string `json:"retry_after,omitempty"`
	Error      string `json:"error,omitempty"`
}

// handleSignUp creates one Supabase account, bootstraps local data, and starts a backend session when possible.
func (a *App) handleSignUp(w http.ResponseWriter, r *http.Request) {
	var requestBody authCredentialsRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	email := strings.TrimSpace(requestBody.Email)
	password := strings.TrimSpace(requestBody.Password)
	name := strings.TrimSpace(requestBody.Name)
	if email == "" || password == "" {
		writeJSONError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	signUpResult, err := a.SupabaseClient.SignUp(r.Context(), email, password, name)
	if err != nil {
		a.writeSupabaseAuthError(w, err)
		return
	}
	if signUpResult.Session == nil {
		writeJSON(w, http.StatusOK, signUpPendingResponse{
			SignupCompletedWithoutSession: true,
			Email:                         email,
		})
		return
	}

	if err := a.finishBackendSignIn(w, r, *signUpResult.Session); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
	}
}

// handleLogin exchanges credentials for a backend-owned auth session.
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var requestBody authCredentialsRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	email := strings.TrimSpace(requestBody.Email)
	password := strings.TrimSpace(requestBody.Password)
	if email == "" || password == "" {
		writeJSONError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	supabaseSession, err := a.SupabaseClient.Login(r.Context(), email, password)
	if err != nil {
		a.writeSupabaseAuthError(w, err)
		return
	}

	if err := a.finishBackendSignIn(w, r, supabaseSession); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
	}
}

// handleOAuthExchange converts a Supabase OAuth session into one backend-owned session.
func (a *App) handleOAuthExchange(w http.ResponseWriter, r *http.Request) {
	var requestBody authOAuthExchangeRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	accessToken := strings.TrimSpace(requestBody.AccessToken)
	refreshToken := strings.TrimSpace(requestBody.RefreshToken)
	expiresAtRaw := strings.TrimSpace(requestBody.ExpiresAt)
	if accessToken == "" || refreshToken == "" || expiresAtRaw == "" {
		writeJSONError(w, http.StatusBadRequest, "access_token, refresh_token, and expires_at are required")
		return
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresAtRaw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid expires_at")
		return
	}

	if err := a.finishBackendSignIn(w, r, internalauth.SupabaseSession{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt.UTC(),
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
	}
}

// handleLogout revokes one backend-owned auth session and clears its cookie.
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := a.SessionManager.RevokeSession(r.Context(), a.SessionManager.SessionTokenFromRequest(r)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	a.SessionManager.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRenewSession renews one valid backend session near its expiry.
func (a *App) handleRenewSession(w http.ResponseWriter, r *http.Request) {
	rawSessionToken := a.SessionManager.SessionTokenFromRequest(r)
	renewal, err := a.SessionManager.RenewSession(r.Context(), rawSessionToken)
	response := sessionRenewalResponse{
		ExpiresAt: renewal.ExpiresAt.Format(time.RFC3339),
	}
	if !renewal.ExpiresAt.IsZero() {
		response.RenewAfter = a.SessionManager.RenewalStartsAt(renewal.ExpiresAt).Format(time.RFC3339)
	}
	if !renewal.RetryAfter.IsZero() {
		response.RetryAfter = renewal.RetryAfter.Format(time.RFC3339)
	}
	if err != nil {
		switch {
		case errors.Is(err, internalauth.ErrSessionRenewalNotDue):
			writeJSON(w, http.StatusOK, response)
		case errors.Is(err, internalauth.ErrSessionRenewalUnavailable):
			response.Error = "session renewal unavailable"
			writeJSON(w, http.StatusConflict, response)
		case errors.Is(err, internalauth.ErrSessionRenewalRetryLater):
			response.Error = "session renewal temporarily unavailable"
			writeJSON(w, http.StatusServiceUnavailable, response)
		case errors.Is(err, internalauth.ErrSessionExpired),
			errors.Is(err, internalauth.ErrSessionRevoked),
			errors.Is(err, internalauth.ErrSessionIdentityMismatch),
			errors.Is(err, pgx.ErrNoRows):
			a.SessionManager.ClearSessionCookie(w)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		default:
			serverError(w, r, err)
		}
		return
	}

	a.SessionManager.SetSessionCookieUntil(w, renewal.RawSessionToken, renewal.ExpiresAt)
	response.Renewed = true
	writeJSON(w, http.StatusOK, response)
}

func (a *App) finishBackendSignIn(w http.ResponseWriter, r *http.Request, supabaseSession internalauth.SupabaseSession) error {
	identity, err := a.AuthVerifier.Verify(supabaseSession.AccessToken)
	if err != nil {
		return err
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, organizations, err := a.ensureUserAndOrganizations(r, queries, identity)
	if err != nil {
		return err
	}

	activeOrganizationID := resolveActiveOrganizationID(pgtype.UUID{}, organizations)
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}

	rawSessionToken, err := a.SessionManager.CreateSession(r.Context(), user.ID, activeOrganizationID, supabaseSession)
	if err != nil {
		return err
	}

	a.SessionManager.SetSessionCookie(w, rawSessionToken)
	meBody := newMeResponse(user, organizations, activeOrganizationID)
	a.attachActiveOrgFeatures(r.Context(), &meBody, activeOrganizationID)
	writeJSON(w, http.StatusOK, meBody)
	return nil
}

func (a *App) writeSupabaseAuthError(w http.ResponseWriter, err error) {
	var authError *internalauth.SupabaseAuthError
	if errors.As(err, &authError) {
		writeJSONError(w, authError.StatusCode, authError.Message)
		return
	}
	log.Printf("auth: supabase request failed: %v", err)
	writeJSONError(w, http.StatusServiceUnavailable, "authentication provider unavailable")
}
