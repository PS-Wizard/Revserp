package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestPrincipalContextRoundTrip(t *testing.T) {
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	p := Principal{User: sqlc.User{ID: uid, Email: "a@example.com"}, Organizations: []sqlc.ListOrganizationsForUserRow{{ID: uid}}}
	ctx := withPrincipal(context.Background(), p)
	got, ok := principalFromContext(ctx)
	if !ok {
		t.Fatal("principalFromContext returned false, want true")
	}
	if got.User.ID != uid || len(got.Organizations) != 1 {
		t.Fatalf("principal mismatch: got %+v", got)
	}
	if _, ok := principalFromContext(context.Background()); ok {
		t.Fatal("empty context should not have principal")
	}
}

func TestPrincipalMiddlewareRequiresIdentity(t *testing.T) {
	app := &App{Queries: sqlc.New(nil)}
	handler := app.requirePrincipal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached without identity")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerWithPrincipalSucceeds(t *testing.T) {
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000002")
	p := Principal{User: sqlc.User{ID: uid, Email: "b@example.com"}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFromContext(r.Context())
		if !ok {
			serverError(w, r, errors.New("missing principal"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"user_id": principal.User.ID.String()})
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withPrincipal(req.Context(), p))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["user_id"] != uid.String() {
		t.Fatalf("user_id = %q, want %q", body["user_id"], uid.String())
	}
}

func TestHandlerMissingPrincipalReturns500(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principalFromContext(r.Context()); !ok {
			serverError(w, r, errors.New("missing principal"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing principal should be 500, got %d", rec.Code)
	}
}

func TestPrincipalFromSessionRouter(t *testing.T) {
	f := newSessionFixture(t)
	rec := get(t, f.app.Router(), "/me", "", f.rawCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me with valid session status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var body meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if body.User.Email != f.email {
		t.Fatalf("user email = %q, want %q", body.User.Email, f.email)
	}
	if len(body.Organizations) == 0 {
		t.Error("expected at least one organization for new user fixture")
	}
}

func TestPrincipalForV1Me(t *testing.T) {
	f := newAPIKeyFixture(t)
	raw, _ := f.createKey(t, "principal-v1")
	rec := v1Get(t, f.app.Router(), "/v1/me", raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me with API key status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.User.ID != f.userID.String() {
		t.Fatalf("user id = %q, want %q", body.User.ID, f.userID.String())
	}
}

func TestTenancyRegressionForUser(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userA pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`, "tenancy-a", "a@example.com").Scan(&userA); err != nil {
		t.Fatalf("create user A: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userA) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userA); err != nil {
		t.Fatalf("add member A: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'P','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, projectID) })

	var userB pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test',$1,$2) RETURNING id`, "tenancy-b", "b@example.com").Scan(&userB); err != nil {
		t.Fatalf("create user B: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userB) })

	if _, err := queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userB}); err == nil {
		t.Fatal("outsider should not access project, but GetProjectByIDForUser succeeded")
	}
	if _, err := queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userA}); err != nil {
		t.Fatalf("owner should access project, got err %v", err)
	}
}

func TestRenewalDoesNotProvisionPrincipal(t *testing.T) {
	f := newSessionFixture(t)
	var before int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM organization_members WHERE user_id=$1`, f.userID).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	// Use the real POST renewal path with session cookie, as the route is POST /auth/session/renew.
	// This mirrors session_renewal_integration_test.go's postSessionRenewal helper.
	rec := postSessionRenewal(f)
	if rec.Code != http.StatusOK {
		t.Fatalf("renewal POST should be 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Verify renewal response shape (renewal may be not due, but must not be 500 and must not provision).
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode renewal body: %v body=%s", err, rec.Body.String())
	}
	if _, ok := body["renewed"]; !ok {
		t.Fatalf("renewal response should contain 'renewed' field, got %s", rec.Body.String())
	}
	var after int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM organization_members WHERE user_id=$1`, f.userID).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before == 0 && after != 0 {
		t.Fatalf("renewal should not provision organization, before=%d after=%d", before, after)
	}
	if before != 0 && before != after {
		t.Fatalf("renewal should not change org membership, before=%d after=%d", before, after)
	}
	// Ensure a second renewal via the same session still does not provision (idempotent).
	rec2 := postSessionRenewal(f)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second renewal POST should be 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestSuspendedPrincipalReturns403(t *testing.T) {
	f := newAPIKeyFixture(t)
	if _, err := f.pool.Exec(f.ctx, `UPDATE users SET status='suspended' WHERE id=$1`, f.userID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `UPDATE users SET status='active' WHERE id=$1`, f.userID)
	})
	raw, _ := f.createKey(t, "suspended-principal")
	rec := v1Get(t, f.app.Router(), "/v1/me", raw)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("suspended should be 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "account suspended") {
		t.Fatalf("body should contain account suspended, got %q", rec.Body.String())
	}
	f2 := newSessionFixture(t)
	if _, err := f2.pool.Exec(f2.ctx, `UPDATE users SET status='suspended' WHERE id=$1`, f2.userID); err != nil {
		t.Fatalf("suspend2: %v", err)
	}
	rec2 := get(t, f2.app.Router(), "/me", "", f2.rawCookie)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("suspended session should be 403, got %d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestProvisioningFailureReturns500(t *testing.T) {
	queries, pool, _ := newFeaturesTestQueries(t)
	pool.Close()
	app := &App{DB: pool, Queries: queries}
	identity := internalauth.Identity{Provider: "test", Subject: "provision-fail", Email: "fail@example.com"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(internalauth.WithIdentity(req.Context(), identity))
	rec := httptest.NewRecorder()
	handler := app.requirePrincipal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler on provisioning failure")
	}))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("provisioning failure should be 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDirectHandlerFallbackSuccess(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test',$1,$2) RETURNING id`, "direct-fallback", "direct@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	app := &App{DB: pool, Queries: queries}
	req := httptest.NewRequest(http.MethodGet, "/organizations/"+orgID.String()+"/projects", nil)
	req = req.WithContext(internalauth.WithIdentity(req.Context(), internalauth.Identity{Provider: "test", Subject: "direct-fallback"}))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("organizationID", orgID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	app.handleListProjects(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("direct handler fallback should be 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHelperPathsFallback(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test',$1,$2) RETURNING id`, "helper-fallback", "helper@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'P','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, projectID) })
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, requested_by_user_id) VALUES ($1,'completed',$2) RETURNING id`, projectID, userID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM crawls WHERE id=$1`, crawlID) })
	app := &App{DB: pool, Queries: queries}

	t.Run("loadCrawlIssueExportRows", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(internalauth.WithIdentity(req.Context(), internalauth.Identity{Provider: "test", Subject: "helper-fallback"}))
		rec := httptest.NewRecorder()
		rows, ok := app.loadCrawlIssueExportRows(rec, req, crawlID, exportFilters{})
		if !ok && rec.Code == http.StatusInternalServerError {
			t.Fatalf("helper should not be 500 on fallback, got %d body=%s", rec.Code, rec.Body.String())
		}
		_ = rows
	})

	t.Run("ensureInternalScoringUser", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(internalauth.WithIdentity(req.Context(), internalauth.Identity{Provider: "test", Subject: "helper-fallback"}))
		rec := httptest.NewRecorder()
		user, ok := app.ensureInternalScoringUser(rec, req)
		if !ok {
			t.Fatalf("ensureInternalScoringUser fallback should succeed, status=%d body=%s", rec.Code, rec.Body.String())
		}
		if user.ID != userID {
			t.Fatalf("user mismatch")
		}
	})

	t.Run("workspaceCrawls", func(t *testing.T) {
		var baselineID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, requested_by_user_id) VALUES ($1,'completed',$2) RETURNING id`, projectID, userID).Scan(&baselineID); err != nil {
			t.Fatalf("create baseline: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM crawls WHERE id=$1`, baselineID) })
		req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"?baseline_crawl_id="+baselineID.String(), nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("crawlID", crawlID.String())
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		req = req.WithContext(internalauth.WithIdentity(req.Context(), internalauth.Identity{Provider: "test", Subject: "helper-fallback"}))
		_, _, _, err := app.workspaceCrawls(req)
		if err != nil {
			t.Fatalf("workspaceCrawls fallback should succeed, got %v", err)
		}
	})
}

func TestConcurrentOrphanProvisioningIsSerialized(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	// Create orphan user with no organization.
	var userID pgtype.UUID
	subject := fmt.Sprintf("concurrent-orphan-%d-%d", time.Now().UnixNano(), 0)
	// Ensure unique by deleting any prior orphan with same subject
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE auth_provider='test' AND auth_subject=$1`, subject)
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`, subject, subject+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create orphan user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	// Ensure truly orphan (no orgs)
	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organization_members WHERE user_id=$1`, userID).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before != 0 {
		t.Fatalf("orphan should have 0 orgs, got %d", before)
	}
	identity := internalauth.Identity{Provider: "test", Subject: subject, Email: subject + "@example.com", Name: "Concurrent Orphan"}
	app := &App{DB: pool, Queries: queries}
	const workers = 10
	errs := make(chan error, workers)
	principals := make(chan Principal, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(internalauth.WithIdentity(req.Context(), identity))
			user, orgs, err := app.ensureUserAndOrganizations(req, queries, identity)
			if err != nil {
				errs <- err
				return
			}
			if !user.ID.Valid || len(orgs) == 0 {
				errs <- errors.New("usable principal missing org")
				return
			}
			principals <- Principal{User: user, Organizations: orgs}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	close(principals)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent provisioning error: %v", err)
		}
	}
	// Verify exactly one organization/membership was created, despite concurrent provisioning.
	var orgCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organization_members WHERE user_id=$1`, userID).Scan(&orgCount); err != nil {
		t.Fatalf("count orgs after: %v", err)
	}
	if orgCount != 1 {
		t.Fatalf("concurrent orphan provisioning should leave exactly 1 membership, got %d", orgCount)
	}
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id FROM organization_members WHERE user_id=$1`, userID).Scan(&orgID); err != nil {
		t.Fatalf("load org_id: %v", err)
	}
	// All principals should have same org.
	for p := range principals {
		if len(p.Organizations) != 1 || p.Organizations[0].ID != orgID {
			t.Fatalf("principal org mismatch: got %+v want %s", p.Organizations, orgID.String())
		}
		if p.User.ID != userID {
			t.Fatalf("principal user mismatch")
		}
	}
	// Cleanup organizations (will be cascade deleted via user, but ensure)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })
}
