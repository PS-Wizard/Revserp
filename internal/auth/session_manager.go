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

	"github.com/ps-wizard/revserp/internal/crypto"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const sessionRefreshSkew = time.Minute

// SessionManager manages backend-owned auth sessions backed by Postgres.
type SessionManager struct {
	queries        *sqlc.Queries
	verifier       *Verifier
	supabaseClient *SupabaseClient
	cookieName     string
	cookieDomain   string
	sessionTTL     time.Duration
	cookieSecure   bool
	crypter        *crypto.Crypter
	refreshGroup   singleflight.Group
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
	crypter *crypto.Crypter,
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
		crypter:        crypter,
	}
}

// encryptToken encrypts a token for storage. Returns the plaintext unchanged on crypter absence.
func (manager *SessionManager) encryptToken(token string) (string, error) {
	if manager.crypter == nil || token == "" {
		return token, nil
	}
	return manager.crypter.Encrypt(token)
}

// decryptToken decrypts a stored token. Falls back to plaintext if decryption fails (legacy compat).
func (manager *SessionManager) decryptToken(stored string) string {
	if manager.crypter == nil || stored == "" {
		return stored
	}
	plaintext, err := manager.crypter.Decrypt(stored)
	if err != nil {
		// Legacy plaintext fallback: existing sessions stored before encryption was introduced.
		log.Printf("auth: token decryption failed, treating as legacy plaintext (session will re-encrypt on next refresh): %v", err)
		return stored
	}
	return plaintext
}

// CreateSession stores one backend session and returns the raw cookie token.
func (manager *SessionManager) CreateSession(ctx context.Context, userID pgtype.UUID, activeOrgID pgtype.UUID, supabaseSession SupabaseSession) (string, error) {
	rawSessionToken, err := generateSessionToken()
	if err != nil {
		return "", err
	}

	encAccessToken, err := manager.encryptToken(supabaseSession.AccessToken)
	if err != nil {
		return "", fmt.Errorf("encrypt supabase access token: %w", err)
	}
	encRefreshToken, err := manager.encryptToken(supabaseSession.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("encrypt supabase refresh token: %w", err)
	}

	if _, err := manager.queries.CreateSession(ctx, sqlc.CreateSessionParams{
		UserID:                       userID,
		SessionTokenHash:             hashSessionToken(rawSessionToken),
		SupabaseAccessToken:          encAccessToken,
		SupabaseRefreshToken:         encRefreshToken,
		SupabaseAccessTokenExpiresAt: timestamptzValue(supabaseSession.ExpiresAt),
		ActiveOrgID:                  activeOrgID,
		ExpiresAt:                    timestamptzValue(time.Now().UTC().Add(manager.sessionTTL)),
	}); err != nil {
		return "", fmt.Errorf("create backend session: %w", err)
	}

	return rawSessionToken, nil
}

// refreshResult is the shared result type for singleflight token refreshes.
type refreshResult struct {
	accessToken          string
	refreshToken         string
	accessTokenExpiresAt time.Time
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

	// Decrypt tokens from storage (with legacy plaintext fallback).
	accessToken := manager.decryptToken(sessionRow.SupabaseAccessToken)
	refreshToken := manager.decryptToken(sessionRow.SupabaseRefreshToken)
	accessTokenExpiresAt := sessionRow.SupabaseAccessTokenExpiresAt.Time.UTC()

	if accessTokenExpiresAt.Before(time.Now().UTC().Add(sessionRefreshSkew)) {
		// Serialize concurrent refreshes for the same session via singleflight.
		sfKey := sessionRow.ID.String()
		v, err, _ := manager.refreshGroup.Do(sfKey, func() (any, error) {
			refreshedSession, refreshErr := manager.supabaseClient.Refresh(ctx, refreshToken)
			if refreshErr != nil {
				var authErr *SupabaseAuthError
				if errors.As(refreshErr, &authErr) && authErr.StatusCode >= 400 && authErr.StatusCode < 500 {
					_ = manager.queries.RevokeSession(ctx, sessionRow.ID)
				} else {
					log.Printf("infra: refresh supabase session (transient, session kept): session=%s error=%v", sessionRow.ID.String(), refreshErr)
				}
				return nil, fmt.Errorf("refresh supabase session: %w", refreshErr)
			}

			newAccessToken := refreshedSession.AccessToken
			newRefreshToken := refreshedSession.RefreshToken
			newExpiresAt := refreshedSession.ExpiresAt.UTC()

			encAccessToken, encErr := manager.encryptToken(newAccessToken)
			if encErr != nil {
				return nil, fmt.Errorf("encrypt refreshed access token: %w", encErr)
			}
			encRefreshToken, encErr := manager.encryptToken(newRefreshToken)
			if encErr != nil {
				return nil, fmt.Errorf("encrypt refreshed refresh token: %w", encErr)
			}

			if dbErr := manager.queries.UpdateSessionTokens(ctx, sqlc.UpdateSessionTokensParams{
				ID:                           sessionRow.ID,
				SupabaseAccessToken:          encAccessToken,
				SupabaseRefreshToken:         encRefreshToken,
				SupabaseAccessTokenExpiresAt: timestamptzValue(newExpiresAt),
			}); dbErr != nil {
				return nil, fmt.Errorf("update refreshed backend session: %w", dbErr)
			}

			return &refreshResult{
				accessToken:          newAccessToken,
				refreshToken:         newRefreshToken,
				accessTokenExpiresAt: newExpiresAt,
			}, nil
		})
		if err != nil {
			return Identity{}, SessionContext{}, err
		}
		result := v.(*refreshResult)
		accessToken = result.accessToken
		accessTokenExpiresAt = result.accessTokenExpiresAt
		_ = accessTokenExpiresAt
	} else {
		if err := manager.queries.UpdateSessionLastUsedAt(ctx, sessionRow.ID); err != nil {
			log.Printf("infra: update session last_used_at (non-fatal): session=%s error=%v", sessionRow.ID.String(), err)
		}
	}

	identity, err := manager.verifier.Verify(accessToken)
	if err != nil {
		return Identity{}, SessionContext{}, fmt.Errorf("verify stored supabase access token: %w", err)
	}

	return identity, SessionContext{
		SessionID:   sessionRow.ID,
		UserID:      sessionRow.UserID,
		ActiveOrgID: sessionRow.ActiveOrgID,
	}, nil
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
func (manager *SessionManager) UpdateActiveOrganization(ctx context.Context, sessionID pgtype.UUID, userID pgtype.UUID, activeOrgID pgtype.UUID) error {
	if !sessionID.Valid {
		return errors.New("missing session id")
	}
	if err := manager.queries.UpdateSessionActiveOrganization(ctx, sqlc.UpdateSessionActiveOrganizationParams{
		ID:          sessionID,
		ActiveOrgID: activeOrgID,
		UserID:      userID,
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
