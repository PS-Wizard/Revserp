package app

import (
	"context"
	"errors"
	"net/http"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Principal is the authenticated request identity with local user and organizations.
// It is resolved once per authenticated request and stored in request context.
type Principal struct {
	User          sqlc.User
	Organizations []sqlc.ListOrganizationsForUserRow
}

type principalContextKey struct{}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

func principalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

// requirePrincipal resolves the local user and organizations exactly once per
// authenticated request, after token verification and active-user checks.
// It uses ensureUserAndOrganizations to preserve provisioning behavior.
func (a *App) requirePrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := internalauth.IdentityFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		user, organizations, err := a.ensureUserAndOrganizations(r, a.Queries, identity)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if user.Status != "active" {
			writeJSONError(w, http.StatusForbidden, "account suspended")
			return
		}
		p := Principal{User: user, Organizations: organizations}
		ctx := withPrincipal(r.Context(), p)
		// Keep cached user for requireActiveUser/resolveUser compatibility.
		ctx = context.WithValue(ctx, cachedUserContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// getPrincipal returns the Principal for protected handlers, writing a 500
// when the request did not pass through requirePrincipal. It falls back to
// ensureCurrentUser for direct-handler tests that inject identity without
// routing through middleware, preserving those tests while keeping the happy
// path free of an extra DB lookup.
func (a *App) getPrincipal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if p, ok := principalFromContext(r.Context()); ok && p.User.ID.Valid {
		return p, true
	}
	user, orgs, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil || !user.ID.Valid {
		serverError(w, r, errors.New("missing principal"))
		return Principal{}, false
	}
	return Principal{User: user, Organizations: orgs}, true
}
