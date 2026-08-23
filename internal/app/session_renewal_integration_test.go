package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

func TestSessionRenewalRotatesOnceAndKeepsBriefTokenGrace(t *testing.T) {
	fixture := newSessionFixture(t)
	accessToken := mintTestAccessToken(t, fixture.privateKey, authSubjectForUser(t, fixture), fixture.email)
	client, refreshCalls := renewalSupabaseClient(t, http.StatusOK, `{"access_token":"`+accessToken+`","refresh_token":"rotated-refresh","expires_in":3600}`)
	fixture.app.SessionManager = internalauth.NewSessionManager(fixture.pool, fixture.verifier, client, "", "", time.Hour, false)
	setSessionNearExpiry(t, fixture)

	first := postSessionRenewal(fixture)
	if first.Code != http.StatusOK {
		t.Fatalf("first renewal status = %d, body = %s", first.Code, first.Body.String())
	}
	cookies := first.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == "" || cookies[0].Value == fixture.rawCookie {
		t.Fatalf("renewal did not rotate the session cookie")
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls after first renewal = %d, want 1", got)
	}

	if _, _, err := fixture.app.SessionManager.AuthenticateRequest(fixture.ctx, fixture.rawCookie); err != nil {
		t.Fatalf("old cookie rejected during grace period: %v", err)
	}
	if _, _, err := fixture.app.SessionManager.AuthenticateRequest(fixture.ctx, cookies[0].Value); err != nil {
		t.Fatalf("rotated cookie rejected: %v", err)
	}

	second := postSessionRenewal(fixture)
	if second.Code != http.StatusOK {
		t.Fatalf("second renewal status = %d, body = %s", second.Code, second.Body.String())
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls after duplicate renewal = %d, want 1", got)
	}
	if len(second.Result().Cookies()) != 0 {
		t.Fatal("duplicate renewal overwrote the rotated cookie")
	}
}

func TestConcurrentSessionRenewalUsesOneSupabaseRefresh(t *testing.T) {
	fixture := newSessionFixture(t)
	accessToken := mintTestAccessToken(t, fixture.privateKey, authSubjectForUser(t, fixture), fixture.email)
	client, refreshCalls := renewalSupabaseClient(t, http.StatusOK, `{"access_token":"`+accessToken+`","refresh_token":"rotated-refresh","expires_in":3600}`)
	fixture.app.SessionManager = internalauth.NewSessionManager(fixture.pool, fixture.verifier, client, "", "", time.Hour, false)
	setSessionNearExpiry(t, fixture)

	const callers = 5
	start := make(chan struct{})
	errorsByCaller := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := fixture.app.SessionManager.RenewSession(fixture.ctx, fixture.rawCookie)
			errorsByCaller <- err
		}()
	}
	ready.Wait()
	close(start)

	renewed := 0
	notDue := 0
	for range callers {
		err := <-errorsByCaller
		switch {
		case err == nil:
			renewed++
		case errors.Is(err, internalauth.ErrSessionRenewalNotDue):
			notDue++
		default:
			t.Fatalf("concurrent renewal error: %v", err)
		}
	}
	if renewed != 1 || notDue != callers-1 {
		t.Fatalf("renewed = %d, not due = %d; want 1 and %d", renewed, notDue, callers-1)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestInvalidRefreshTokenDisablesFurtherRenewal(t *testing.T) {
	fixture := newSessionFixture(t)
	client, refreshCalls := renewalSupabaseClient(t, http.StatusBadRequest, `{"msg":"Invalid Refresh Token: Refresh Token Not Found"}`)
	fixture.app.SessionManager = internalauth.NewSessionManager(fixture.pool, fixture.verifier, client, "", "", time.Hour, false)
	setSessionNearExpiry(t, fixture)

	for attempt := 1; attempt <= 2; attempt++ {
		response := postSessionRenewal(fixture)
		if response.Code != http.StatusConflict {
			t.Fatalf("renewal attempt %d status = %d, want %d", attempt, response.Code, http.StatusConflict)
		}
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if _, _, err := fixture.app.SessionManager.AuthenticateRequest(fixture.ctx, fixture.rawCookie); err != nil {
		t.Fatalf("valid backend session ended after refresh failure: %v", err)
	}
}

func TestSessionRenewalKeepsRotatedRefreshTokenWhenVerificationIsTransient(t *testing.T) {
	fixture := newSessionFixture(t)
	client, refreshCalls := renewalSupabaseClient(t, http.StatusOK, `{"access_token":"not-a-jwt","refresh_token":"rotated-refresh","expires_in":3600}`)
	fixture.app.SessionManager = internalauth.NewSessionManager(fixture.pool, fixture.verifier, client, "", "", time.Hour, false)
	setSessionNearExpiry(t, fixture)

	response := postSessionRenewal(fixture)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("renewal status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	var storedRefreshToken string
	var retryScheduled bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT supabase_refresh_token, supabase_refresh_retry_after IS NOT NULL
		FROM sessions
		WHERE user_id = $1`, fixture.userID).Scan(&storedRefreshToken, &retryScheduled); err != nil {
		t.Fatalf("load refreshed session state: %v", err)
	}
	if storedRefreshToken != "rotated-refresh" || !retryScheduled {
		t.Fatalf("stored refresh token = %q, retry scheduled = %t", storedRefreshToken, retryScheduled)
	}
	if _, _, err := fixture.app.SessionManager.AuthenticateRequest(fixture.ctx, fixture.rawCookie); err != nil {
		t.Fatalf("valid backend session ended after verification failure: %v", err)
	}
}

func TestSessionRenewalRevokesIdentityMismatch(t *testing.T) {
	fixture := newSessionFixture(t)
	accessToken := mintTestAccessToken(t, fixture.privateKey, "different-user", fixture.email)
	client, refreshCalls := renewalSupabaseClient(t, http.StatusOK, `{"access_token":"`+accessToken+`","refresh_token":"rotated-refresh","expires_in":3600}`)
	fixture.app.SessionManager = internalauth.NewSessionManager(fixture.pool, fixture.verifier, client, "", "", time.Hour, false)
	setSessionNearExpiry(t, fixture)

	response := postSessionRenewal(fixture)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("renewal status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	_, _, err := fixture.app.SessionManager.AuthenticateRequest(fixture.ctx, fixture.rawCookie)
	if !errors.Is(err, internalauth.ErrSessionRevoked) {
		t.Fatalf("authentication error = %v, want revoked session", err)
	}
}

func TestSessionRenewalRejectsSuspendedUserWithoutSupabase(t *testing.T) {
	fixture := newSessionFixture(t)
	client, refreshCalls := renewalSupabaseClient(t, http.StatusInternalServerError, `{}`)
	fixture.app.SessionManager = internalauth.NewSessionManager(fixture.pool, fixture.verifier, client, "", "", time.Hour, false)
	setSessionNearExpiry(t, fixture)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE users SET status = 'suspended' WHERE id = $1`, fixture.userID); err != nil {
		t.Fatalf("suspend user: %v", err)
	}

	response := postSessionRenewal(fixture)
	if response.Code != http.StatusForbidden {
		t.Fatalf("renewal status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if got := refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0", got)
	}
}

func renewalSupabaseClient(t *testing.T, status int, body string) (*internalauth.SupabaseClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return internalauth.NewSupabaseClient(server.URL, "test-anon-key"), &calls
}

func authSubjectForUser(t *testing.T, fixture sessionFixture) string {
	t.Helper()
	var subject string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT auth_subject FROM users WHERE id = $1`, fixture.userID).Scan(&subject); err != nil {
		t.Fatalf("load auth subject: %v", err)
	}
	return subject
}

func setSessionNearExpiry(t *testing.T, fixture sessionFixture) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE sessions
		SET expires_at = now() + interval '10 minutes',
			supabase_refresh_retry_after = NULL,
			supabase_refresh_disabled_at = NULL,
			revoked_at = NULL
		WHERE user_id = $1`, fixture.userID); err != nil {
		t.Fatalf("set session near expiry: %v", err)
	}
}

func postSessionRenewal(fixture sessionFixture) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/auth/session/renew", nil)
	request.AddCookie(&http.Cookie{Name: "revserp_session", Value: fixture.rawCookie})
	response := httptest.NewRecorder()
	fixture.app.Router().ServeHTTP(response, request)
	return response
}
