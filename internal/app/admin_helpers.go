package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// isPlatformAdmin returns true if either the persisted admin flag is set or the user's email
// has the @revketer.ai suffix (bootstrap fallback).
func isPlatformAdmin(email string, dbFlag bool) bool {
	if dbFlag {
		return true
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(email)), "@revketer.ai")
}

// requirePlatformAdmin is middleware that returns 403 if the authenticated user is not a platform admin.
func (a *App) requirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := internalauth.IdentityFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Use the user already fetched by requireActiveUser if available.
		var user sqlc.User
		if cached, ok := r.Context().Value(cachedUserContextKey{}).(sqlc.User); ok && cached.ID.Valid {
			user = cached
		} else {
			row, err := a.Queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
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
			user = userRowToUser(row)
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
// It stores the resolved user in the request context so downstream middleware and handlers
// can reuse it without an additional DB round-trip.
func (a *App) requireActiveUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := internalauth.IdentityFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		row, err := a.Queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
			AuthProvider: identity.Provider,
			AuthSubject:  identity.Subject,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				next.ServeHTTP(w, r)
				return
			}
			serverError(w, r, err)
			return
		}

		if row.Status != "active" {
			writeJSONError(w, http.StatusForbidden, "account suspended")
			return
		}

		// Cache the resolved user for the rest of this request.
		ctx := context.WithValue(r.Context(), cachedUserContextKey{}, userRowToUser(row))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// currentUserID returns the authenticated user's UUID from the request context.
func (a *App) currentUserID(r *http.Request) (pgtype.UUID, error) {
	// Use the user already fetched by requireActiveUser if available.
	if cached, ok := r.Context().Value(cachedUserContextKey{}).(sqlc.User); ok && cached.ID.Valid {
		return cached.ID, nil
	}

	identity, ok := internalauth.IdentityFromContext(r.Context())
	if !ok {
		return pgtype.UUID{}, errors.New("missing identity")
	}

	row, err := a.Queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
		AuthProvider: identity.Provider,
		AuthSubject:  identity.Subject,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}

	return row.ID, nil
}
