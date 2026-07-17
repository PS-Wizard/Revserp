package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const sessionRefreshSkew = time.Minute
const lastUsedThrottle = 5 * time.Minute

// SessionManager manages backend-owned auth sessions backed by Postgres.
type SessionManager struct {
	queries        *sqlc.Queries
	verifier       *Verifier
	supabaseClient *SupabaseClient
	cookieName     string
	cookieDomain   string
	sessionTTL     time.Duration
	cookieSecure   bool
	// refreshGroup collapses concurrent Supabase token refreshes for the same
	// session into a single call. Supabase rotates refresh tokens and revokes
	// the whole token family if an old one is reused, so racing requests that
	// each refresh independently would revoke the session and force re-login.
	refreshGroup singleflight.Group
}

// NewSessionManager builds a backend session manager.
func NewSessionManager(
	pool *pgxpool.Pool,
	verifier *Verifier,
	supabaseClient *SupabaseClient,
	cookieName string,
	cookieDomain string,
	sessionTTL time.Duration,
	cookieSecure bool,
) *SessionManager {
	if strings.TrimSpace(cookieName) == "" {
		cookieName = "revserp_session"
	}
	cookieDomain = strings.TrimSpace(cookieDomain)
	if sessionTTL <= 0 {
		sessionTTL = 30 * 24 * time.Hour
	}

	return &SessionManager{
		queries:        sqlc.New(pool),
		verifier:       verifier,
		supabaseClient: supabaseClient,
		cookieName:     cookieName,
		cookieDomain:   cookieDomain,
		sessionTTL:     sessionTTL,
		cookieSecure:   cookieSecure,
	}
}

// CreateSession stores one backend session and returns the raw cookie token.
func (manager *SessionManager) CreateSession(ctx context.Context, userID pgtype.UUID, activeOrgID pgtype.UUID, supabaseSession SupabaseSession) (string, error) {
	rawSessionToken, err := generateSessionToken()
	if err != nil {
		return "", err
	}

	if _, err := manager.queries.CreateSession(ctx, sqlc.CreateSessionParams{
		UserID:                       userID,
		SessionTokenHash:             hashSessionToken(rawSessionToken),
		SupabaseAccessToken:          supabaseSession.AccessToken,
		SupabaseRefreshToken:         supabaseSession.RefreshToken,
		SupabaseAccessTokenExpiresAt: timestamptzValue(supabaseSession.ExpiresAt),
		ActiveOrgID:                  activeOrgID,
		ExpiresAt:                    timestamptzValue(time.Now().UTC().Add(manager.sessionTTL)),
	}); err != nil {
		return "", fmt.Errorf("create backend session: %w", err)
	}

	return rawSessionToken, nil
}

// AuthenticateRequest resolves one backend session cookie into auth identity and session context.
func (manager *SessionManager) AuthenticateRequest(ctx context.Context, rawSessionToken string) (Identity, SessionContext, error) {
	if strings.TrimSpace(rawSessionToken) == "" {
		return Identity{}, SessionContext{}, errors.New("missing session token")
	}

	sessionRow, err := manager.queries.GetSessionByTokenHash(ctx, hashSessionToken(rawSessionToken))
	if err != nil {
		return Identity{}, SessionContext{}, fmt.Errorf("load backend session: %w", err)
	}
	if sessionRow.RevokedAt.Valid {
		return Identity{}, SessionContext{}, errors.New("session revoked")
	}
	if sessionRow.ExpiresAt.Valid && time.Now().UTC().After(sessionRow.ExpiresAt.Time) {
		return Identity{}, SessionContext{}, errors.New("session expired")
	}

	accessToken := sessionRow.SupabaseAccessToken
	accessTokenExpiresAt := sessionRow.SupabaseAccessTokenExpiresAt.Time.UTC()

	sessionContext := SessionContext{
		SessionID:   sessionRow.ID,
		UserID:      sessionRow.UserID,
		ActiveOrgID: sessionRow.ActiveOrgID,
	}

	if !sessionRow.SupabaseAccessTokenExpiresAt.Valid || accessTokenExpiresAt.Before(time.Now().UTC().Add(sessionRefreshSkew)) {
		type refreshedTokens struct {
			accessToken string
			expiresAt   time.Time
		}
		resolved, refreshErr, _ := manager.refreshGroup.Do(sessionRow.ID.String(), func() (any, error) {
			// Re-read inside the singleflight so the refresh always uses the freshest
			// stored refresh token. Supabase rotates refresh tokens and rejects a
			// reused one; a token captured before the flight (already rotated by an
			// earlier, non-overlapping request) would otherwise trigger that rejection.
			current, err := manager.queries.GetSessionByTokenHash(ctx, hashSessionToken(rawSessionToken))
			if err != nil {
				return nil, fmt.Errorf("reload backend session for refresh: %w", err)
			}
			// A prior request may have already refreshed the token; reuse its result.
			if current.SupabaseAccessTokenExpiresAt.Valid && current.SupabaseAccessTokenExpiresAt.Time.UTC().After(time.Now().UTC().Add(sessionRefreshSkew)) {
				return refreshedTokens{accessToken: current.SupabaseAccessToken, expiresAt: current.SupabaseAccessTokenExpiresAt.Time.UTC()}, nil
			}
			refreshedSession, refreshErr := manager.supabaseClient.Refresh(ctx, current.SupabaseRefreshToken)
			if refreshErr != nil {
				// The backend session is the durable authority (see CreateSession and
				// sessionTTL); a Supabase refresh failure must not end it. Never revoke
				// here — report so the caller falls back to the last-known-good identity.
				log.Printf("infra: refresh supabase session failed (session kept): session=%s error=%v", sessionRow.ID.String(), refreshErr)
				return nil, refreshErr
			}
			if err := manager.queries.UpdateSessionTokens(ctx, sqlc.UpdateSessionTokensParams{
				ID:                           sessionRow.ID,
				SupabaseAccessToken:          refreshedSession.AccessToken,
				SupabaseRefreshToken:         refreshedSession.RefreshToken,
				SupabaseAccessTokenExpiresAt: timestamptzValue(refreshedSession.ExpiresAt.UTC()),
			}); err != nil {
				return nil, fmt.Errorf("update refreshed backend session: %w", err)
			}
			return refreshedTokens{accessToken: refreshedSession.AccessToken, expiresAt: refreshedSession.ExpiresAt.UTC()}, nil
		})
		if refreshErr != nil {
			// Refresh failed (transient Supabase error, or a rotated/reused refresh
			// token). Fall back to the identity in the last-known-good access token
			// rather than forcing re-login on an otherwise valid backend session.
			identity, idErr := manager.verifier.VerifyAllowingExpired(accessToken)
			if idErr != nil {
				return Identity{}, SessionContext{}, fmt.Errorf("resolve identity after refresh failure: %w", idErr)
			}
			return identity, sessionContext, nil
		}
		accessToken = resolved.(refreshedTokens).accessToken
	} else {
		if !sessionRow.LastUsedAt.Valid || time.Since(sessionRow.LastUsedAt.Time) > lastUsedThrottle {
			if err := manager.queries.UpdateSessionLastUsedAt(ctx, sessionRow.ID); err != nil {
				log.Printf("infra: update session last_used_at (non-fatal): session=%s error=%v", sessionRow.ID.String(), err)
			}
		}
	}

	identity, err := manager.verifier.Verify(accessToken)
	if err != nil {
		return Identity{}, SessionContext{}, fmt.Errorf("verify stored supabase access token: %w", err)
	}

	return identity, sessionContext, nil
}

// RevokeSession revokes one backend session token if it exists.
func (manager *SessionManager) RevokeSession(ctx context.Context, rawSessionToken string) error {
	if strings.TrimSpace(rawSessionToken) == "" {
		return nil
	}

	sessionRow, err := manager.queries.GetSessionByTokenHash(ctx, hashSessionToken(rawSessionToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load backend session for revoke: %w", err)
	}
	if err := manager.queries.RevokeSession(ctx, sessionRow.ID); err != nil {
		return fmt.Errorf("revoke backend session: %w", err)
	}

	return nil
}

// UpdateActiveOrganization persists the current organization context for one backend session.
func (manager *SessionManager) UpdateActiveOrganization(ctx context.Context, sessionID pgtype.UUID, activeOrgID pgtype.UUID) error {
	if !sessionID.Valid {
		return errors.New("missing session id")
	}
	if err := manager.queries.UpdateSessionActiveOrganization(ctx, sqlc.UpdateSessionActiveOrganizationParams{
		ID:          sessionID,
		ActiveOrgID: activeOrgID,
	}); err != nil {
		return fmt.Errorf("update session active organization: %w", err)
	}
	return nil
}

// SetSessionCookie writes the backend session cookie to the response.
func (manager *SessionManager) SetSessionCookie(w http.ResponseWriter, rawSessionToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     manager.cookieName,
		Value:    rawSessionToken,
		Path:     "/",
		Domain:   manager.cookieDomain,
		HttpOnly: true,
		Secure:   manager.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().UTC().Add(manager.sessionTTL),
	})
}

// ClearSessionCookie removes the backend session cookie from the response.
func (manager *SessionManager) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     manager.cookieName,
		Value:    "",
		Path:     "/",
		Domain:   manager.cookieDomain,
		HttpOnly: true,
		Secure:   manager.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
	})
}

// SessionTokenFromRequest extracts the raw backend session token from one request cookie.
func (manager *SessionManager) SessionTokenFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(manager.cookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func generateSessionToken() (string, error) {
	rawTokenBytes := make([]byte, 32)
	if _, err := rand.Read(rawTokenBytes); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(rawTokenBytes), nil
}

func hashSessionToken(rawSessionToken string) string {
	tokenHash := sha256.Sum256([]byte(rawSessionToken))
	return hex.EncodeToString(tokenHash[:])
}

func timestamptzValue(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
