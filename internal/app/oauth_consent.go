package app

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

// The consent screen is part of the MCP authorization flow. Supabase Auth is the
// authorization server and redirects the browser to our consent page with an
// authorization_id. These two handlers proxy the Supabase calls that page needs,
// using the Supabase access token already stored on the backend session, so the
// browser never holds a Supabase session of its own.

// handleGetOAuthAuthorization returns the pending authorization request so the
// consent screen can show which client asks for what. Supabase answers with a
// redirect_url instead when the user already granted the same scopes, and the
// page follows that immediately.
func (a *App) handleGetOAuthAuthorization(w http.ResponseWriter, r *http.Request) {
	authorizationID, ok := oauthAuthorizationID(w, r)
	if !ok {
		return
	}
	accessToken, ok := a.supabaseUserAccessToken(w, r)
	if !ok {
		return
	}

	authorization, err := a.SupabaseClient.GetOAuthAuthorization(r.Context(), accessToken, authorizationID)
	if err != nil {
		writeSupabaseOAuthError(w, "load authorization", err)
		return
	}

	// The Supabase shape is the wire contract for the consent page, so it is
	// passed through unchanged rather than mapped into a second struct.
	writeJSON(w, http.StatusOK, authorization)
}

// oAuthConsentBody is the decision the consent screen submits.
type oAuthConsentBody struct {
	Action string `json:"action"`
}

// handlePostOAuthConsent forwards one approve or deny decision and returns the
// URL the browser must follow to hand control back to the OAuth client.
func (a *App) handlePostOAuthConsent(w http.ResponseWriter, r *http.Request) {
	authorizationID, ok := oauthAuthorizationID(w, r)
	if !ok {
		return
	}

	var body oAuthConsentBody
	if !readJSONOrRespond(w, r, &body) {
		return
	}
	action := strings.TrimSpace(body.Action)
	if action != internalauth.OAuthConsentApprove && action != internalauth.OAuthConsentDeny {
		writeJSONError(w, http.StatusBadRequest, "action must be approve or deny")
		return
	}

	accessToken, ok := a.supabaseUserAccessToken(w, r)
	if !ok {
		return
	}

	consent, err := a.SupabaseClient.SubmitOAuthConsent(r.Context(), accessToken, authorizationID, action)
	if err != nil {
		writeSupabaseOAuthError(w, "submit consent", err)
		return
	}
	if strings.TrimSpace(consent.RedirectURL) == "" {
		writeJSONError(w, http.StatusBadGateway, "authorization server returned no redirect url")
		return
	}

	writeJSON(w, http.StatusOK, consent)
}

// oauthAuthorizationID reads the authorization id from the path. Supabase
// issues opaque ids (not UUIDs), so this only rejects empty or junk values
// before we forward the id upstream.
func oauthAuthorizationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	authorizationID := strings.TrimSpace(chi.URLParam(r, "authorizationID"))
	if !validOAuthAuthorizationID(authorizationID) {
		writeJSONError(w, http.StatusBadRequest, "invalid authorization id")
		return "", false
	}
	return authorizationID, true
}

func validOAuthAuthorizationID(id string) bool {
	n := len(id)
	if n < 16 || n > 128 {
		return false
	}
	for i := 0; i < n; i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// supabaseUserAccessToken loads a usable Supabase access token for the current
// backend session, refreshing it when the stored one has expired.
func (a *App) supabaseUserAccessToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	rawSessionToken := a.SessionManager.SessionTokenFromRequest(r)
	accessToken, err := a.SessionManager.FreshUserAccessToken(r.Context(), rawSessionToken)
	if err != nil {
		log.Printf("oauth consent: supabase access token unavailable: %v", err)
		_ = a.SessionManager.RevokeSession(r.Context(), rawSessionToken)
		a.SessionManager.ClearSessionCookie(w)
		writeJSONError(w, http.StatusUnauthorized, "session expired, sign in again")
		return "", false
	}
	return accessToken, true
}

// writeSupabaseOAuthError maps one Supabase failure onto our status codes. The
// upstream message is not echoed back except for a rejected request, which is
// the one case the user can act on.
func writeSupabaseOAuthError(w http.ResponseWriter, stage string, err error) {
	var authError *internalauth.SupabaseAuthError
	if errors.As(err, &authError) {
		log.Printf("oauth consent: %s failed: status=%d message=%q", stage, authError.StatusCode, authError.Message)
		switch authError.StatusCode {
		case http.StatusNotFound:
			writeJSONError(w, http.StatusNotFound, "authorization request not found or expired")
			return
		case http.StatusBadRequest:
			writeJSONError(w, http.StatusBadRequest, authError.Message)
			return
		case http.StatusUnauthorized, http.StatusForbidden:
			writeJSONError(w, http.StatusUnauthorized, "session expired, sign in again")
			return
		}
	}

	log.Printf("oauth consent: %s failed: %v", stage, err)
	writeJSONError(w, http.StatusBadGateway, "authorization server unavailable")
}
