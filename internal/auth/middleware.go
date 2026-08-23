package auth

import (
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// RequireSession resolves a backend-owned auth session and stores its identity in the request context.
func RequireSession(sessionManager *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawSessionToken := sessionManager.SessionTokenFromRequest(r)
			if rawSessionToken == "" {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}

			identity, session, err := sessionManager.AuthenticateRequest(r.Context(), rawSessionToken)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrSessionRevoked) {
					sessionManager.ClearSessionCookie(w)
					http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}
				log.Printf("auth: request authentication failed: %v", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}

			ctxWithIdentity := WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(WithSession(ctxWithIdentity, session)))
		})
	}
}
