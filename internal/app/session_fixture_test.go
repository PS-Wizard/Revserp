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
)

// Shared cookie-session fixture for app tests. The Supabase JWKS is faked
// with a local signing key so no external calls happen.

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

func get(t *testing.T, handler http.Handler, path, rawCookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rawCookie != "" {
		req.AddCookie(&http.Cookie{Name: "revserp_session", Value: rawCookie})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
