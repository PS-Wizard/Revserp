package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type apiKeyFixture struct {
	app    *App
	ctx    context.Context
	pool   *pgxpool.Pool
	userID pgtype.UUID
	email  string
}

func newAPIKeyFixture(t *testing.T) apiKeyFixture {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	name := fmt.Sprintf("api-key-int-test-%d", time.Now().UnixNano())
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// api_keys and agent_setup_codes both reference users(id) ON DELETE CASCADE,
	// so deleting the user removes every credential created by a test.
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})
	app := &App{DB: pool, Queries: queries, APIKeyManager: internalauth.NewAPIKeyManager(queries)}
	return apiKeyFixture{app: app, ctx: ctx, pool: pool, userID: userID, email: name + "@example.com"}
}

func (f apiKeyFixture) createKey(t *testing.T, name string) (raw, id string) {
	t.Helper()
	rawKey, prefix, hash, err := f.app.APIKeyManager.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	row, err := f.app.Queries.CreateAPIKey(f.ctx, sqlc.CreateAPIKeyParams{
		UserID:      f.userID,
		Name:        name,
		TokenPrefix: prefix,
		TokenHash:   hash,
	})
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return rawKey, row.ID.String()
}

func v1Get(t *testing.T, handler http.Handler, path, rawKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rawKey != "" {
		req.Header.Set("Authorization", "Bearer "+rawKey)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAPIKeysV1Integration(t *testing.T) {
	f := newAPIKeyFixture(t)
	router := f.app.Router()

	t.Run("valid key authenticates GET /v1/me", func(t *testing.T) {
		raw, _ := f.createKey(t, "me-test")
		rec := v1Get(t, router, "/v1/me", raw)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body struct {
			User struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"user"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.User.Email != f.email || body.User.ID != f.userID.String() {
			t.Errorf("/v1/me user = %+v, want email %q id %s", body.User, f.email, f.userID.String())
		}
	})

	t.Run("unknown and revoked keys are rejected with 401", func(t *testing.T) {
		raw, id := f.createKey(t, "revoke-me")
		if rec := v1Get(t, router, "/v1/me", "rvs_live_definitelynotarealkeyvalue0000000000"); rec.Code != http.StatusUnauthorized {
			t.Errorf("unknown key status = %d, want 401", rec.Code)
		}
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE api_keys SET revoked_at = now() WHERE id = $1`, mustParseUUID(t, id)); err != nil {
			t.Fatalf("revoke key: %v", err)
		}
		if rec := v1Get(t, router, "/v1/me", raw); rec.Code != http.StatusUnauthorized {
			t.Errorf("revoked key status = %d, want 401", rec.Code)
		}
		if rec := v1Get(t, router, "/v1/me", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("missing Authorization header status = %d, want 401", rec.Code)
		}
	})

	t.Run("suspended user gets 403 account suspended", func(t *testing.T) {
		raw, _ := f.createKey(t, "suspend-me")
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE users SET status = 'suspended' WHERE id = $1`, f.userID); err != nil {
			t.Fatalf("suspend user: %v", err)
		}
		rec := v1Get(t, router, "/v1/me", raw)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "account suspended") {
			t.Errorf("body = %q, want account suspended message", rec.Body.String())
		}
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE users SET status = 'active' WHERE id = $1`, f.userID); err != nil {
			t.Fatalf("reactivate user: %v", err)
		}
	})

	t.Run("last_used_at is throttled within five minutes and updates when stale", func(t *testing.T) {
		raw, id := f.createKey(t, "touch-test")
		keyID := mustParseUUID(t, id)
		if rec := v1Get(t, router, "/v1/me", raw); rec.Code != http.StatusOK {
			t.Fatalf("first use status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var first time.Time
		if err := f.pool.QueryRow(f.ctx,
			`SELECT last_used_at FROM api_keys WHERE id = $1`, keyID).Scan(&first); err != nil {
			t.Fatalf("read last_used_at: %v", err)
		}
		// Immediate repeat requests must not move last_used_at (5-minute throttle).
		for range 3 {
			if rec := v1Get(t, router, "/v1/me", raw); rec.Code != http.StatusOK {
				t.Fatalf("throttled use status = %d", rec.Code)
			}
		}
		var unchanged time.Time
		if err := f.pool.QueryRow(f.ctx,
			`SELECT last_used_at FROM api_keys WHERE id = $1`, keyID).Scan(&unchanged); err != nil {
			t.Fatalf("reread last_used_at: %v", err)
		}
		if !unchanged.Equal(first) {
			t.Errorf("last_used_at moved within five minutes: %v -> %v", first, unchanged)
		}
		// Backdate past the throttle window; the next request must update it.
		if _, err := f.pool.Exec(f.ctx,
			`UPDATE api_keys SET last_used_at = now() - interval '6 minutes' WHERE id = $1`, keyID); err != nil {
			t.Fatalf("backdate last_used_at: %v", err)
		}
		if rec := v1Get(t, router, "/v1/me", raw); rec.Code != http.StatusOK {
			t.Fatalf("stale use status = %d", rec.Code)
		}
		var updated time.Time
		if err := f.pool.QueryRow(f.ctx,
			`SELECT last_used_at FROM api_keys WHERE id = $1`, keyID).Scan(&updated); err != nil {
			t.Fatalf("read updated last_used_at: %v", err)
		}
		if !updated.After(first) {
			t.Errorf("stale last_used_at was not refreshed: %v -> %v", first, updated)
		}
	})
}

func TestAgentSetupCodeIntegration(t *testing.T) {
	f := newAPIKeyFixture(t)
	router := f.app.Router()

	createSetupCode := func(t *testing.T, ttl time.Duration) (rawCode, codeID string) {
		t.Helper()
		raw, hash, err := f.app.APIKeyManager.GenerateSetupCode()
		if err != nil {
			t.Fatalf("generate setup code: %v", err)
		}
		var id pgtype.UUID
		if err := f.pool.QueryRow(f.ctx,
			`INSERT INTO agent_setup_codes (user_id, name, code_hash, expires_at)
			 VALUES ($1, 'AI agent', $2, $3) RETURNING id`,
			f.userID, hash, time.Now().UTC().Add(ttl)).Scan(&id); err != nil {
			t.Fatalf("insert setup code: %v", err)
		}
		return raw, id.String()
	}

	redeem := func(rawCode string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/agent/setup",
			strings.NewReader(fmt.Sprintf(`{"code":%q}`, rawCode)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("expired setup code is rejected", func(t *testing.T) {
		rawCode, _ := createSetupCode(t, -time.Second)
		if rec := redeem(rawCode); rec.Code != http.StatusUnauthorized {
			t.Fatalf("expired code status = %d, want 401, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("redemption is one-use", func(t *testing.T) {
		rawCode, codeID := createSetupCode(t, agentSetupCodeTTL)
		first := redeem(rawCode)
		if first.Code != http.StatusOK || !strings.HasPrefix(first.Body.String(), "Authorization: Bearer rvs_live_") {
			t.Fatalf("first redemption returned an invalid response: status = %d", first.Code)
		}
		rawFirstKey := strings.TrimSpace(strings.TrimPrefix(first.Body.String(), "Authorization: Bearer "))
		if rec := v1Get(t, router, "/v1/me", rawFirstKey); rec.Code != http.StatusOK {
			t.Fatalf("redeemed key does not authenticate: status = %d", rec.Code)
		}
		if second := redeem(rawCode); second.Code != http.StatusUnauthorized {
			t.Errorf("second redemption status = %d, want 401", second.Code)
		}
		var redeemedAt *time.Time
		if err := f.pool.QueryRow(f.ctx,
			`SELECT redeemed_at FROM agent_setup_codes WHERE id = $1`,
			mustParseUUID(t, codeID)).Scan(&redeemedAt); err != nil {
			t.Fatalf("read redeemed_at: %v", err)
		}
		if redeemedAt == nil {
			t.Error("setup code was not marked redeemed")
		}
		var keyCount int
		if err := f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM api_keys WHERE user_id = $1`, f.userID).Scan(&keyCount); err != nil {
			t.Fatalf("count keys: %v", err)
		}
		if keyCount != 1 {
			t.Errorf("api keys after double redemption = %d, want 1", keyCount)
		}
	})

	t.Run("concurrent redemptions create exactly one permanent key", func(t *testing.T) {
		var before int
		if err := f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM api_keys WHERE user_id = $1`, f.userID).Scan(&before); err != nil {
			t.Fatalf("count keys before race: %v", err)
		}
		rawCode, _ := createSetupCode(t, agentSetupCodeTTL)
		const racers = 5
		responses := make([]*httptest.ResponseRecorder, racers)
		var wg sync.WaitGroup
		for i := range racers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				responses[i] = redeem(rawCode)
			}(i)
		}
		wg.Wait()
		winners := 0
		for _, rec := range responses {
			if rec.Code == http.StatusOK {
				winners++
			} else if rec.Code != http.StatusUnauthorized {
				t.Errorf("racer got unexpected status %d, body = %s", rec.Code, rec.Body.String())
			}
		}
		if winners != 1 {
			t.Errorf("successful concurrent redemptions = %d, want 1", winners)
		}
		var keyCount int
		if err := f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM api_keys WHERE user_id = $1`, f.userID).Scan(&keyCount); err != nil {
			t.Fatalf("count keys: %v", err)
		}
		if keyCount-before != 1 {
			t.Errorf("api keys created by race = %d, want exactly 1", keyCount-before)
		}
	})
}

func TestAPIKeyListAndRevokeAuthorizationIntegration(t *testing.T) {
	f := newAPIKeyFixture(t)
	other := newAPIKeyFixture(t)
	mineRaw, _ := f.createKey(t, "mine")

	t.Run("list never leaks secrets and stays scoped to owner", func(t *testing.T) {
		other.createKey(t, "theirs")

		req := httptest.NewRequest(http.MethodGet, "/api-keys", nil)
		req = req.WithContext(internalauth.WithIdentity(req.Context(), internalauth.Identity{
			Provider: "test",
			Subject:  strings.TrimSuffix(f.email, "@example.com"),
			Email:    f.email,
		}))
		rec := httptest.NewRecorder()
		f.app.handleListAPIKeys(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, secret := range []string{"token_hash", "tokenHash"} {
			if strings.Contains(body, secret) {
				t.Errorf("list response exposes %q field: %s", secret, body)
			}
		}
		if strings.Contains(body, mineRaw) {
			t.Error("list response contains the full raw API key")
		}
		if strings.Contains(body, `"name":"theirs"`) {
			t.Error("list response includes another user's API keys")
		}
		var parsed struct {
			APIKeys []map[string]json.RawMessage `json:"api_keys"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		if len(parsed.APIKeys) == 0 {
			t.Fatal("list is empty for the owning user")
		}
		for i, key := range parsed.APIKeys {
			if _, ok := key["token_hash"]; ok {
				t.Errorf("key %d exposes token_hash field", i)
			}
		}
	})

	t.Run("user cannot revoke another user's key", func(t *testing.T) {
		theirRaw, theirID := other.createKey(t, "not-yours")
		req := revokeRequest(t, theirID, f.email)
		rec := httptest.NewRecorder()
		f.app.handleRevokeAPIKey(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-user revoke status = %d, want 404, body = %s", rec.Code, rec.Body.String())
		}
		if r := v1Get(t, f.app.Router(), "/v1/me", theirRaw); r.Code != http.StatusOK {
			t.Errorf("other user's key should still authenticate, status = %d", r.Code)
		}
	})

	t.Run("owned revoke is idempotent", func(t *testing.T) {
		raw, id := f.createKey(t, "self-revoke")
		revoke := func() *httptest.ResponseRecorder {
			req := revokeRequest(t, id, f.email)
			rec := httptest.NewRecorder()
			f.app.handleRevokeAPIKey(rec, req)
			return rec
		}
		if rec := revoke(); rec.Code != http.StatusOK {
			t.Fatalf("first revoke status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if rec := revoke(); rec.Code != http.StatusOK {
			t.Errorf("second revoke status = %d, want 200 (idempotent), body = %s", rec.Code, rec.Body.String())
		}
		if r := v1Get(t, f.app.Router(), "/v1/me", raw); r.Code != http.StatusUnauthorized {
			t.Errorf("revoked own key still authenticates, status = %d", r.Code)
		}
	})
}

func revokeRequest(t *testing.T, apiKeyID, callerEmail string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api-keys/"+apiKeyID+"/revoke", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("apiKeyID", apiKeyID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	return req.WithContext(internalauth.WithIdentity(req.Context(), internalauth.Identity{
		Provider: "test",
		Subject:  strings.TrimSuffix(callerEmail, "@example.com"),
	}))
}

func mustParseUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := parseUUIDParam(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return id
}
