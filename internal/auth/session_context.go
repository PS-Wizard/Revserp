package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

type sessionContextKey struct{}

// SessionContext holds the authenticated backend session details for one request.
type SessionContext struct {
	SessionID   pgtype.UUID
	UserID      pgtype.UUID
	ActiveOrgID pgtype.UUID
}

// WithSession stores the authenticated backend session in the context.
func WithSession(ctx context.Context, session SessionContext) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, session)
}

// SessionFromContext loads the authenticated backend session from the context.
func SessionFromContext(ctx context.Context) (SessionContext, bool) {
	session, ok := ctx.Value(sessionContextKey{}).(SessionContext)
	return session, ok
}
