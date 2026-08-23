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

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	lastUsedThrottle        = 5 * time.Minute
	sessionRenewBefore      = 24 * time.Hour
	sessionRenewRetryDelay  = 5 * time.Minute
	previousSessionTokenTTL = time.Minute
)

var (
	ErrSessionExpired            = errors.New("session expired")
	ErrSessionRevoked            = errors.New("session revoked")
	ErrSessionRenewalNotDue      = errors.New("session renewal not due")
	ErrSessionRenewalUnavailable = errors.New("session renewal unavailable")
	ErrSessionRenewalRetryLater  = errors.New("session renewal retry later")
	ErrSessionIdentityMismatch   = errors.New("session identity mismatch")
)

// SessionRenewal reports the result of one explicit session renewal.
type SessionRenewal struct {
	Renewed         bool
	RawSessionToken string
	ExpiresAt       time.Time
	RetryAfter      time.Time
}

// SessionManager manages backend-owned auth sessions backed by Postgres.
type SessionManager struct {
	pool           *pgxpool.Pool
	queries        *sqlc.Queries
	verifier       *Verifier
	supabaseClient *SupabaseClient
	cookieName     string
	cookieDomain   string
	sessionTTL     time.Duration
	cookieSecure   bool
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
		pool:           pool,
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

// AuthenticateRequest resolves one backend session cookie using only local state.
func (manager *SessionManager) AuthenticateRequest(ctx context.Context, rawSessionToken string) (Identity, SessionContext, error) {
	if strings.TrimSpace(rawSessionToken) == "" {
		return Identity{}, SessionContext{}, errors.New("missing session token")
	}

	sessionRow, err := manager.queries.GetSessionByTokenHash(ctx, hashSessionToken(rawSessionToken))
	if err != nil {
		return Identity{}, SessionContext{}, fmt.Errorf("load backend session: %w", err)
	}
	now := time.Now().UTC()
	if err := validateSession(sessionRow.RevokedAt, sessionRow.ExpiresAt, now); err != nil {
		return Identity{}, SessionContext{}, err
	}

	if !sessionRow.LastUsedAt.Valid || now.Sub(sessionRow.LastUsedAt.Time.UTC()) > lastUsedThrottle {
		if err := manager.queries.UpdateSessionLastUsedAt(ctx, sessionRow.ID); err != nil {
			log.Printf("infra: update session last_used_at (non-fatal): session=%s error=%v", sessionRow.ID.String(), err)
		}
	}

	name := ""
	if sessionRow.Name.Valid {
		name = sessionRow.Name.String
	}
	return Identity{
			Provider: sessionRow.AuthProvider,
			Subject:  sessionRow.AuthSubject,
			Email:    sessionRow.Email,
			Name:     name,
		}, SessionContext{
			SessionID:   sessionRow.ID,
			UserID:      sessionRow.UserID,
			ActiveOrgID: sessionRow.ActiveOrgID,
			ExpiresAt:   sessionRow.ExpiresAt.Time.UTC(),
		}, nil
}

// RenewSession refreshes Supabase only near backend-session expiry and rotates the cookie token.
func (manager *SessionManager) RenewSession(ctx context.Context, rawSessionToken string) (SessionRenewal, error) {
	if strings.TrimSpace(rawSessionToken) == "" {
		return SessionRenewal{}, errors.New("missing session token")
	}

	tx, err := manager.pool.Begin(ctx)
	if err != nil {
		return SessionRenewal{}, fmt.Errorf("begin session renewal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := manager.queries.WithTx(tx)
	sessionRow, err := queries.GetSessionByTokenHashForUpdate(ctx, hashSessionToken(rawSessionToken))
	if err != nil {
		return SessionRenewal{}, fmt.Errorf("load backend session for renewal: %w", err)
	}

	now := time.Now().UTC()
	if err := validateSession(sessionRow.RevokedAt, sessionRow.ExpiresAt, now); err != nil {
		return SessionRenewal{}, err
	}
	result := SessionRenewal{ExpiresAt: sessionRow.ExpiresAt.Time.UTC()}
	if sessionRow.SupabaseRefreshDisabledAt.Valid {
		return result, ErrSessionRenewalUnavailable
	}
	if sessionRow.SupabaseRefreshRetryAfter.Valid && now.Before(sessionRow.SupabaseRefreshRetryAfter.Time.UTC()) {
		result.RetryAfter = sessionRow.SupabaseRefreshRetryAfter.Time.UTC()
		return result, ErrSessionRenewalRetryLater
	}
	if now.Add(manager.renewBefore()).Before(result.ExpiresAt) {
		return result, ErrSessionRenewalNotDue
	}

	if manager.supabaseClient == nil || manager.verifier == nil {
		return result, errors.New("session renewal is not configured")
	}
	refreshedSession, refreshErr := manager.supabaseClient.Refresh(ctx, sessionRow.SupabaseRefreshToken)
	if refreshErr != nil {
		if isPermanentSupabaseRefreshError(refreshErr) {
			if err := queries.DisableSessionRenewal(ctx, sessionRow.ID); err != nil {
				return result, fmt.Errorf("disable session renewal: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return result, fmt.Errorf("commit disabled session renewal: %w", err)
			}
			return result, ErrSessionRenewalUnavailable
		}
		if err := delaySessionRenewal(ctx, tx, queries, sessionRow.ID, &result, now); err != nil {
			return result, err
		}
		return result, ErrSessionRenewalRetryLater
	}

	refreshedIdentity, err := manager.verifier.Verify(refreshedSession.AccessToken)
	if err != nil {
		if saveErr := saveUnverifiedSessionRefresh(ctx, tx, queries, sessionRow.ID, refreshedSession, &result, now); saveErr != nil {
			return result, saveErr
		}
		return result, ErrSessionRenewalRetryLater
	}
	if refreshedIdentity.Provider != sessionRow.AuthProvider || refreshedIdentity.Subject != sessionRow.AuthSubject {
		if revokeErr := queries.RevokeSession(ctx, sessionRow.ID); revokeErr != nil {
			_ = tx.Rollback(ctx)
			if retryErr := manager.queries.RevokeSession(ctx, sessionRow.ID); retryErr != nil {
				return result, fmt.Errorf("revoke mismatched session: %v; retry: %w", revokeErr, retryErr)
			}
			return result, ErrSessionIdentityMismatch
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			if retryErr := manager.queries.RevokeSession(ctx, sessionRow.ID); retryErr != nil {
				return result, fmt.Errorf("commit mismatched session revocation: %v; retry: %w", commitErr, retryErr)
			}
		}
		return result, ErrSessionIdentityMismatch
	}

	rotatedRawSessionToken, err := generateSessionToken()
	if err != nil {
		return result, err
	}
	rotatedSessionTokenHash := hashSessionToken(rotatedRawSessionToken)
	result.ExpiresAt = now.Add(manager.sessionTTL)
	if err := queries.RotateSession(ctx, sqlc.RotateSessionParams{
		ID:                            sessionRow.ID,
		SessionTokenHash:              rotatedSessionTokenHash,
		PreviousSessionTokenExpiresAt: timestamptzValue(now.Add(previousSessionTokenTTL)),
		SupabaseAccessToken:           refreshedSession.AccessToken,
		SupabaseRefreshToken:          refreshedSession.RefreshToken,
		SupabaseAccessTokenExpiresAt:  timestamptzValue(refreshedSession.ExpiresAt.UTC()),
		ExpiresAt:                     timestamptzValue(result.ExpiresAt),
	}); err != nil {
		_ = tx.Rollback(ctx)
		recovered, recoveryErr := manager.recoverRefreshedSession(ctx, rawSessionToken, rotatedSessionTokenHash, sessionRow.ID, refreshedSession, &result, now)
		if recoveryErr != nil {
			return result, fmt.Errorf("rotate renewed session: %v; recovery: %w", err, recoveryErr)
		}
		if !recovered {
			return result, ErrSessionRenewalRetryLater
		}
	} else if err := tx.Commit(ctx); err != nil {
		recovered, recoveryErr := manager.recoverRefreshedSession(ctx, rawSessionToken, rotatedSessionTokenHash, sessionRow.ID, refreshedSession, &result, now)
		if recoveryErr != nil {
			return result, fmt.Errorf("commit session renewal: %v; recovery: %w", err, recoveryErr)
		}
		if !recovered {
			return result, ErrSessionRenewalRetryLater
		}
	}
	result.Renewed = true
	result.RawSessionToken = rotatedRawSessionToken
	return result, nil
}

func delaySessionRenewal(ctx context.Context, tx pgx.Tx, queries *sqlc.Queries, sessionID pgtype.UUID, result *SessionRenewal, now time.Time) error {
	result.RetryAfter = now.Add(sessionRenewRetryDelay)
	if err := queries.DelaySessionRenewal(ctx, sqlc.DelaySessionRenewalParams{
		ID:                        sessionID,
		SupabaseRefreshRetryAfter: timestamptzValue(result.RetryAfter),
	}); err != nil {
		return fmt.Errorf("delay session renewal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delayed session renewal: %w", err)
	}
	return nil
}

func saveUnverifiedSessionRefresh(ctx context.Context, tx pgx.Tx, queries *sqlc.Queries, sessionID pgtype.UUID, refreshedSession SupabaseSession, result *SessionRenewal, now time.Time) error {
	result.RetryAfter = now.Add(sessionRenewRetryDelay)
	if err := queries.SaveUnverifiedSessionRefresh(ctx, sqlc.SaveUnverifiedSessionRefreshParams{
		ID:                           sessionID,
		SupabaseAccessToken:          refreshedSession.AccessToken,
		SupabaseRefreshToken:         refreshedSession.RefreshToken,
		SupabaseAccessTokenExpiresAt: timestamptzValue(refreshedSession.ExpiresAt),
		SupabaseRefreshRetryAfter:    timestamptzValue(result.RetryAfter),
	}); err != nil {
		return fmt.Errorf("save unverified session refresh: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit unverified session refresh: %w", err)
	}
	return nil
}

func (manager *SessionManager) recoverRefreshedSession(ctx context.Context, previousRawSessionToken string, rotatedSessionTokenHash string, sessionID pgtype.UUID, refreshedSession SupabaseSession, result *SessionRenewal, now time.Time) (bool, error) {
	current, err := manager.queries.GetSessionByTokenHash(ctx, hashSessionToken(previousRawSessionToken))
	if err != nil {
		return false, fmt.Errorf("load session rotation state: %w", err)
	}
	if current.SessionTokenHash == rotatedSessionTokenHash {
		return true, nil
	}

	result.RetryAfter = now.Add(sessionRenewRetryDelay)
	if err := manager.queries.SaveUnverifiedSessionRefresh(ctx, sqlc.SaveUnverifiedSessionRefreshParams{
		ID:                           sessionID,
		SupabaseAccessToken:          refreshedSession.AccessToken,
		SupabaseRefreshToken:         refreshedSession.RefreshToken,
		SupabaseAccessTokenExpiresAt: timestamptzValue(refreshedSession.ExpiresAt),
		SupabaseRefreshRetryAfter:    timestamptzValue(result.RetryAfter),
	}); err != nil {
		return false, fmt.Errorf("preserve refreshed session after rotation failure: %w", err)
	}
	return false, nil
}

// RenewalStartsAt returns when the client should request session renewal.
func (manager *SessionManager) RenewalStartsAt(expiresAt time.Time) time.Time {
	return expiresAt.UTC().Add(-manager.renewBefore())
}

func (manager *SessionManager) renewBefore() time.Duration {
	if manager.sessionTTL <= 2*sessionRenewBefore {
		return manager.sessionTTL / 2
	}
	return sessionRenewBefore
}

func validateSession(revokedAt pgtype.Timestamptz, expiresAt pgtype.Timestamptz, now time.Time) error {
	if revokedAt.Valid {
		return ErrSessionRevoked
	}
	if !expiresAt.Valid || !now.Before(expiresAt.Time.UTC()) {
		return ErrSessionExpired
	}
	return nil
}

func isPermanentSupabaseRefreshError(err error) bool {
	var authError *SupabaseAuthError
	if !errors.As(err, &authError) {
		return false
	}
	switch authError.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	default:
		return false
	}
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
	manager.SetSessionCookieUntil(w, rawSessionToken, time.Now().UTC().Add(manager.sessionTTL))
}

// SetSessionCookieUntil writes a backend session cookie with an exact expiry.
func (manager *SessionManager) SetSessionCookieUntil(w http.ResponseWriter, rawSessionToken string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     manager.cookieName,
		Value:    rawSessionToken,
		Path:     "/",
		Domain:   manager.cookieDomain,
		HttpOnly: true,
		Secure:   manager.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt.UTC(),
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
