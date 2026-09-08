package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

func TestFinishBackendSignInProvisionsNewIdentity(t *testing.T) {
	fixture := newSessionFixture(t)
	fixture.app.AuthVerifier = fixture.verifier

	subject := fmt.Sprintf("new-oauth-user-%d", time.Now().UnixNano())
	email := subject + "@example.com"
	accessToken := mintTestAccessToken(t, fixture.privateKey, subject, email)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/oauth/exchange", nil)
	err := fixture.app.finishBackendSignIn(recorder, request, internalauth.SupabaseSession{
		AccessToken:  accessToken,
		RefreshToken: "new-oauth-refresh-token",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("finishBackendSignIn: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", recorder.Code, recorder.Body.String())
	}

	var userID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT id FROM users WHERE auth_provider=$1 AND auth_subject=$2
	`, testAuthProvider, subject).Scan(&userID); err != nil {
		t.Fatalf("load provisioned user: %v", err)
	}

	var organizationID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT org_id FROM organization_members WHERE user_id=$1 AND role='owner'
	`, userID).Scan(&organizationID); err != nil {
		t.Fatalf("load provisioned workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organizationID)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	var sessionCount int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, userID).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("session count = %d, want 1", sessionCount)
	}
	if len(recorder.Result().Cookies()) == 0 {
		t.Fatal("backend session cookie was not set")
	}
}
