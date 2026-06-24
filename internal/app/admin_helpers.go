package app

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// isPlatformAdmin returns true only when the persisted DB admin flag is set.
// The previous email-suffix shortcut (@revketer.ai) has been removed to prevent
// privilege escalation via email address spoofing.
func isPlatformAdmin(email string, dbFlag bool) bool {
	return dbFlag
}

// requirePlatformAdmin is middleware that returns 403 if the authenticated user is not a platform admin.
func (a *App) requirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := internalauth.IdentityFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		user, err := a.Queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
			AuthProvider: identity.Provider,
			AuthSubject:  identity.Subject,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return
			}
			serverError(w, r, err)
			return
		}

		if !isPlatformAdmin(user.Email, user.IsPlatformAdmin) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// platformAdminOnly is a handler wrapper that returns 403 for non-admin callers.
func (a *App) platformAdminOnly(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.requirePlatformAdmin(handler).ServeHTTP(w, r)
	}
}

// requireActiveUser is middleware that ensures the authenticated user is not suspended.
func (a *App) requireActiveUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := internalauth.IdentityFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		user, err := a.Queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
			AuthProvider: identity.Provider,
			AuthSubject:  identity.Subject,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			serverError(w, r, err)
			return
		}

		if user.Status != "active" {
			writeJSONError(w, http.StatusForbidden, "account suspended")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// currentUserID returns the authenticated user's UUID from the request context.
func (a *App) currentUserID(r *http.Request) (pgtype.UUID, error) {
	identity, ok := internalauth.IdentityFromContext(r.Context())
	if !ok {
		return pgtype.UUID{}, errors.New("missing identity")
	}

	user, err := a.Queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
		AuthProvider: identity.Provider,
		AuthSubject:  identity.Subject,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}

	return user.ID, nil
}
