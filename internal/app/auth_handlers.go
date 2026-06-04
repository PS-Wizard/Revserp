package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

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
		a.writeSupabaseAuthError(w, err, http.StatusBadRequest)
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
		a.writeSupabaseAuthError(w, err, http.StatusUnauthorized)
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

func (a *App) finishBackendSignIn(w http.ResponseWriter, r *http.Request, supabaseSession internalauth.SupabaseSession) error {
	identity, err := a.AuthVerifier.Verify(supabaseSession.AccessToken)
	if err != nil {
		return err
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())

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
	writeJSON(w, http.StatusOK, newMeResponse(user, organizations, activeOrganizationID))
	return nil
}

func (a *App) writeSupabaseAuthError(w http.ResponseWriter, err error, fallbackStatusCode int) {
	var authError *internalauth.SupabaseAuthError
	if errors.As(err, &authError) {
		writeJSONError(w, authError.StatusCode, authError.Message)
		return
	}
	writeJSONError(w, fallbackStatusCode, err.Error())
}
