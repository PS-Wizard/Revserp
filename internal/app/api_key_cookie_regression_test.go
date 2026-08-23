package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Regression tests for the auth split: bearer API keys must not open browser
// (session-cookie) routes, and browser cookies must not open /v1 routes.
// The Supabase JWKS is faked with a local signing key so no external calls happen.

const (
	testJWTIssuer    = "https://test-jwks.example/auth/v1"
	testJWTAudience  = "authenticated"
	testAuthProvider = "test-session"
)

type sessionFixture struct {
	app        *App
	ctx        context.Context
	pool       *pgxpool.Pool
	userID     pgtype.UUID
	email      string
	rawCookie  string // raw backend session cookie token
	privateKey *ecdsa.PrivateKey
	verifier   *internalauth.Verifier
}

// newSessionFixture creates a user plus a real backend session. Normal request
// authentication uses the local session and user rows, not the stored Supabase token.
func newSessionFixture(t *testing.T) sessionFixture {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)

	// Local JWKS server backing the JWT verifier.
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	jwksDoc := map[string]any{
		"keys": []map[string]any{{
			"kty": "EC",
			"crv": "P-256",
			"kid": "test-key",
			"alg": "ES256",
			"use": "sig",
			"x":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.X.Bytes()),
			"y":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.Y.Bytes()),
		}},
	}
	jwksBody, _ := json.Marshal(jwksDoc)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody)
	}))
	t.Cleanup(jwksServer.Close)

	verifier, err := internalauth.NewVerifier(ctx, testAuthProvider,
		testJWTIssuer, jwksServer.URL+"/.well-known/jwks.json", testJWTAudience)
	if err != nil {
		t.Fatalf("build verifier: %v", err)
	}

	name := fmt.Sprintf("session-regression-%d", time.Now().UnixNano())
	email := name + "@example.com"
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ($1, $2, $3) RETURNING id`, testAuthProvider, name, email).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// sessions and api_keys both reference users(id) ON DELETE CASCADE.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	sessionManager := internalauth.NewSessionManager(pool, verifier, nil, "", "", time.Hour, false)

	minted := mintTestAccessToken(t, privateKey, name, email)
	rawCookie, err := sessionManager.CreateSession(ctx, userID, pgtype.UUID{}, internalauth.SupabaseSession{
		AccessToken:  minted,
		RefreshToken: "unused-test-refresh-token",
		ExpiresAt:    time.Now().UTC().Add(-time.Minute), // expired provider token must not affect local authentication
	})
	if err != nil {
		t.Fatalf("create backend session: %v", err)
	}

	app := &App{
		DB:             pool,
		Queries:        queries,
		SessionManager: sessionManager,
		APIKeyManager:  internalauth.NewAPIKeyManager(queries),
	}
	return sessionFixture{
		app:        app,
		ctx:        ctx,
		pool:       pool,
		userID:     userID,
		email:      email,
		rawCookie:  rawCookie,
		privateKey: privateKey,
		verifier:   verifier,
	}
}

func mintTestAccessToken(t *testing.T, key *ecdsa.PrivateKey, subject, email string) string {
	t.Helper()
	claims := internalauth.SupabaseClaims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "test-jti",
			Subject:   subject,
			Issuer:    testJWTIssuer,
			Audience:  jwt.ClaimStrings{testJWTAudience},
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return signed
}

func (f sessionFixture) createAPIKey(t *testing.T, name string) string {
	t.Helper()
	manager := internalauth.NewAPIKeyManager(f.app.Queries)
	rawKey, prefix, hash, err := manager.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	if _, err := f.app.Queries.CreateAPIKey(f.ctx, sqlc.CreateAPIKeyParams{
		UserID:      f.userID,
		Name:        name,
		TokenPrefix: prefix,
		TokenHash:   hash,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return rawKey
}

func get(t *testing.T, handler http.Handler, path, rawAPIKey, rawCookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rawAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+rawAPIKey)
	}
	if rawCookie != "" {
		req.AddCookie(&http.Cookie{Name: "revserp_session", Value: rawCookie})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// The new auth must not let a bearer API key open browser-only routes.
func TestAPIKeyCannotOpenBrowserRoutesIntegration(t *testing.T) {
	f := newSessionFixture(t)
	raw := f.createAPIKey(t, "browser-route-probe")
	rec := get(t, f.app.Router(), "/api-keys", raw, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api-keys with bearer API key status = %d, want 401, body = %s",
			rec.Code, rec.Body.String())
	}
}

// The new auth must not let a browser cookie open /v1 API routes.
func TestBrowserCookieCannotOpenV1RoutesIntegration(t *testing.T) {
	f := newSessionFixture(t)
	rec := get(t, f.app.Router(), "/v1/me", "", f.rawCookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/me with browser cookie status = %d, want 401, body = %s",
			rec.Code, rec.Body.String())
	}
}

// An existing valid browser session must keep working on browser routes.
func TestValidBrowserSessionStillWorksIntegration(t *testing.T) {
	f := newSessionFixture(t)
	f.createAPIKey(t, "listed-via-cookie")
	rec := get(t, f.app.Router(), "/api-keys", "", f.rawCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api-keys with valid session cookie status = %d, want 200, body = %s",
			rec.Code, rec.Body.String())
	}
	var parsed struct {
		APIKeys []map[string]json.RawMessage `json:"api_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(parsed.APIKeys) == 0 {
		t.Error("valid browser session listed no API keys despite one existing")
	}
}
