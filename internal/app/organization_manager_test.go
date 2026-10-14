package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/organizationaccess"
)

// TestWorkspaceManagerStatusFlags pins the can-manage matrix shared by every
// workspace status endpoint: owners and managers manage, members read,
// platform admin members manage, and outsiders never get a writable flag.
func TestWorkspaceManagerStatusFlags(t *testing.T) {
	tests := []struct {
		name            string
		role            string
		isPlatformAdmin bool
		wantManage      bool
	}{
		{name: "owner manages", role: "owner", wantManage: true},
		{name: "manager manages without admin flag", role: "manager", wantManage: true},
		{name: "member reads", role: "member", wantManage: false},
		{name: "platform admin member manages", role: "member", isPlatformAdmin: true, wantManage: true},
		{name: "outsider never manages", role: "", wantManage: false},
		{name: "platform admin outsider never manages", role: "", isPlatformAdmin: true, wantManage: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := organizationaccess.CanManageOrganization(tc.role, tc.isPlatformAdmin); got != tc.wantManage {
				t.Fatalf("CanManageOrganization(%q, %v) = %v, want %v", tc.role, tc.isPlatformAdmin, got, tc.wantManage)
			}
			if got := locationWorkspaceCanManage(tc.role, tc.isPlatformAdmin); got != tc.wantManage {
				t.Fatalf("locationWorkspaceCanManage(%q, %v) = %v, want %v", tc.role, tc.isPlatformAdmin, got, tc.wantManage)
			}
		})
	}
}

// TestLocationKeywordsAdminFlag shows the keyword status flag reads the
// already-loaded principal instead of an extra user query, defaulting to
// non-admin when no principal is present.
func TestLocationKeywordsAdminFlag(t *testing.T) {
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")

	plain, _ := http.NewRequest(http.MethodGet, "/", nil)
	if locationKeywordsAdmin(plain) {
		t.Fatal("request without principal must not read as admin")
	}

	user := sqlc.User{ID: userID, Email: "manager@example.com", IsPlatformAdmin: true}
	adminReq, _ := http.NewRequest(http.MethodGet, "/", nil)
	adminReq = adminReq.WithContext(withPrincipal(adminReq.Context(), Principal{User: user}))
	if !locationKeywordsAdmin(adminReq) {
		t.Fatal("request with admin principal must read as admin")
	}
}

// TestInviteRoleResponses pins the invite role contract: invites expose the
// granted role, defaulting to member for rows created before roles existed.
func TestInviteRoleResponses(t *testing.T) {
	managerInvite := sqlc.OrganizationInvite{Role: "manager"}
	if got := newOrganizationInviteResponse(managerInvite).Role; got != "manager" {
		t.Fatalf("manager invite response role = %q, want manager", got)
	}
	memberInvite := sqlc.OrganizationInvite{Role: "member"}
	if got := newOrganizationInviteResponse(memberInvite).Role; got != "member" {
		t.Fatalf("member invite response role = %q, want member", got)
	}
	if got := organizationaccess.NormalizeInviteRole(""); got != "member" {
		t.Fatalf("missing invite role defaults to %q, want member", got)
	}
	for _, role := range []string{"owner", "admin", "platform_admin"} {
		if organizationaccess.ValidInviteRole(role) {
			t.Errorf("invite role %q must be rejected", role)
		}
	}
}

// TestManagerHasNoPlatformAdminAccess proves a workspace manager cannot reach
// platform-only backend endpoints: the global admin guard still requires the
// platform admin flag.
func TestManagerHasNoPlatformAdminAccess(t *testing.T) {
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	app := &App{}

	manager := Principal{User: sqlc.User{ID: userID, Email: "manager@example.com"}}
	managerReq := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	managerReq = managerReq.WithContext(withPrincipal(managerReq.Context(), manager))
	managerRec := httptest.NewRecorder()
	app.requirePlatformAdmin(next).ServeHTTP(managerRec, managerReq)
	if managerRec.Code != http.StatusForbidden {
		t.Fatalf("manager platform access status = %d, want 403", managerRec.Code)
	}

	admin := Principal{User: sqlc.User{ID: userID, Email: "admin@example.com", IsPlatformAdmin: true}}
	adminReq := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	adminReq = adminReq.WithContext(withPrincipal(adminReq.Context(), admin))
	adminRec := httptest.NewRecorder()
	app.requirePlatformAdmin(next).ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin platform access status = %d, want 200", adminRec.Code)
	}
}

type workspaceManagerFixture struct {
	app           *App
	pool          *pgxpool.Pool
	ctx           context.Context
	orgID         pgtype.UUID
	projectID     pgtype.UUID
	ownerID       pgtype.UUID
	managerID     pgtype.UUID
	memberID      pgtype.UUID
	adminMemberID pgtype.UUID
	adminOutsider pgtype.UUID
	outsiderID    pgtype.UUID
}

// newWorkspaceManagerFixture connects to an explicit disposable test database
// only. It never falls back to DATABASE_URL, so these tests cannot mutate a
// development or production database.
func newWorkspaceManagerFixture(t *testing.T) workspaceManagerFixture {
	t.Helper()
	databaseURL := os.Getenv("ORGANIZATION_ACCESS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ORGANIZATION_ACCESS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("organization access test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"organizations", "users", "organization_members", "organization_invites", "projects"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.`+table+`')::text`).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("table %s is not migrated in the test database", table)
		}
	}
	var hasInviteRole bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='organization_invites' AND column_name='role')`).Scan(&hasInviteRole); err != nil || !hasInviteRole {
		t.Skip("organization_invites.role is not migrated (needs migration 000112) in the test database")
	}
	queries := sqlc.New(pool)
	name := fmt.Sprintf("org-access-test-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(tag, role string, isAdmin bool) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email, is_platform_admin)
			VALUES ('org-access-test', $1, $2, $3) RETURNING id`, name+tag, name+tag+"@example.com", isAdmin).Scan(&userID); err != nil {
			t.Fatalf("create user %s: %v", tag, err)
		}
		if role != "" {
			if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,$3)`, orgID, userID, role); err != nil {
				t.Fatalf("add %s: %v", tag, err)
			}
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
		return userID
	}
	fx := workspaceManagerFixture{
		app:           &App{DB: pool, Queries: queries},
		pool:          pool,
		ctx:           ctx,
		orgID:         orgID,
		ownerID:       newUser("owner", "owner", false),
		managerID:     newUser("manager", "manager", false),
		memberID:      newUser("member", "member", false),
		adminMemberID: newUser("adminmember", "member", true),
		adminOutsider: newUser("adminoutsider", "", true),
		outsiderID:    newUser("outsider", "", false),
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'org-access-test','https://org-access.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	fx.projectID = projectID
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, projectID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
	})
	return fx
}

func (fx workspaceManagerFixture) principal(userID pgtype.UUID, isAdmin bool) Principal {
	return Principal{User: sqlc.User{ID: userID, Email: "test@example.com", IsPlatformAdmin: isAdmin}}
}

// TestWorkspaceManagerGuardBoundaries proves the shared management guard
// against real membership rows: owners, managers, and platform admin members
// succeed, while members, outsiders, platform admin outsiders, and unknown
// users are denied.
func TestWorkspaceManagerGuardBoundaries(t *testing.T) {
	fx := newWorkspaceManagerFixture(t)
	queries := fx.app.Queries

	for name, tc := range map[string]struct {
		userID  pgtype.UUID
		wantErr bool
	}{
		"owner succeeds":                 {userID: fx.ownerID},
		"manager succeeds":               {userID: fx.managerID},
		"platform admin member succeeds": {userID: fx.adminMemberID},
		"member denied":                  {userID: fx.memberID, wantErr: true},
		"outsider denied":                {userID: fx.outsiderID, wantErr: true},
		"platform admin outsider denied": {userID: fx.adminOutsider, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := requireOrganizationManager(fx.ctx, queries, fx.orgID, tc.userID)
			if tc.wantErr && err == nil {
				t.Fatal("expected forbidden, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
		})
	}

	t.Run("unknown user denied", func(t *testing.T) {
		var unknown pgtype.UUID
		_ = unknown.Scan("00000000-0000-0000-0000-000000000099")
		if err := requireOrganizationManager(fx.ctx, queries, fx.orgID, unknown); err == nil {
			t.Fatal("expected forbidden for unknown user, got nil")
		}
	})

	t.Run("suspended admin member denied", func(t *testing.T) {
		if _, err := fx.pool.Exec(fx.ctx, `UPDATE users SET status='suspended' WHERE id=$1`, fx.adminMemberID); err != nil {
			t.Fatalf("suspend user: %v", err)
		}
		t.Cleanup(func() {
			_, _ = fx.pool.Exec(context.Background(), `UPDATE users SET status='active' WHERE id=$1`, fx.adminMemberID)
		})
		if err := requireOrganizationManager(fx.ctx, queries, fx.orgID, fx.adminMemberID); err == nil {
			t.Fatal("expected forbidden for suspended admin member, got nil")
		}
	})
}

// TestWorkspaceManagerBusinessProfile proves managers and platform admin
// members can write the business profile, members cannot, and outsiders stay
// denied through the existing project scoping.
func TestWorkspaceManagerBusinessProfile(t *testing.T) {
	fx := newWorkspaceManagerFixture(t)
	var regclass string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT to_regclass('public.project_business_profiles')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("project_business_profiles is not migrated in the test database")
	}
	body := `{"brand_name":"Acme","website_url":"https://acme.example"}`
	callUpsert := func(userID pgtype.UUID, isAdmin bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("projectID", fx.projectID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = withPrincipal(ctx, fx.principal(userID, isAdmin))
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		fx.app.handleUpsertProjectBusinessProfile(rr, req)
		return rr
	}

	if rr := callUpsert(fx.ownerID, false); rr.Code != http.StatusOK {
		t.Fatalf("owner upsert status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if rr := callUpsert(fx.managerID, false); rr.Code != http.StatusOK {
		t.Fatalf("manager upsert status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if rr := callUpsert(fx.adminMemberID, true); rr.Code != http.StatusOK {
		t.Fatalf("admin member upsert status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if rr := callUpsert(fx.memberID, false); rr.Code != http.StatusForbidden {
		t.Fatalf("member upsert status = %d, want 403", rr.Code)
	}
	if rr := callUpsert(fx.adminOutsider, true); rr.Code != http.StatusNotFound {
		t.Fatalf("admin outsider upsert status = %d, want 404 through project scoping", rr.Code)
	}

	callStatus := func(userID pgtype.UUID, isAdmin bool) (int, projectBusinessProfileStatusResponse) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("projectID", fx.projectID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = withPrincipal(ctx, fx.principal(userID, isAdmin))
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		fx.app.handleProjectBusinessProfile(rr, req)
		var response projectBusinessProfileStatusResponse
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode status: %v body=%s", err, rr.Body.String())
			}
		}
		return rr.Code, response
	}
	if code, response := callStatus(fx.managerID, false); code != http.StatusOK || !response.CanManageProfile {
		t.Errorf("manager status = %d can_manage=%v, want 200 true", code, response.CanManageProfile)
	}
	if code, response := callStatus(fx.adminMemberID, true); code != http.StatusOK || !response.CanManageProfile {
		t.Errorf("admin member status = %d can_manage=%v, want 200 true", code, response.CanManageProfile)
	}
	if code, response := callStatus(fx.memberID, false); code != http.StatusOK || response.CanManageProfile {
		t.Errorf("member status = %d can_manage=%v, want 200 false", code, response.CanManageProfile)
	}
}

// TestWorkspaceManagerConnections proves the connection status flag follows
// the same management rule for managers, members, and admin outsiders.
func TestWorkspaceManagerConnections(t *testing.T) {
	fx := newWorkspaceManagerFixture(t)
	var regclass string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT to_regclass('public.google_connections')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("google_connections is not migrated in the test database")
	}
	callStatus := func(userID pgtype.UUID, isAdmin bool) (int, bool) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("projectID", fx.projectID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = withPrincipal(ctx, fx.principal(userID, isAdmin))
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		fx.app.handleProjectGSCStatus(rr, req)
		canManage := false
		if rr.Code == http.StatusOK {
			var decoded struct {
				CanManageConnection bool `json:"can_manage_connection"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode gsc status: %v body=%s", err, rr.Body.String())
			}
			canManage = decoded.CanManageConnection
		}
		return rr.Code, canManage
	}
	if code, canManage := callStatus(fx.managerID, false); code != http.StatusOK || !canManage {
		t.Errorf("manager gsc status = %d can_manage=%v, want 200 true", code, canManage)
	}
	if code, canManage := callStatus(fx.memberID, false); code != http.StatusOK || canManage {
		t.Errorf("member gsc status = %d can_manage=%v, want 200 false", code, canManage)
	}
	if code, _ := callStatus(fx.adminOutsider, true); code != http.StatusForbidden {
		t.Errorf("admin outsider gsc status = %d, want 403", code)
	}
}

// TestWorkspaceManagerInvites proves the invite role flow: managers create
// member and manager invites, old clients default to member, owner grants are
// rejected, and redeeming installs the saved role without ever downgrading an
// existing owner or manager.
func TestWorkspaceManagerInvites(t *testing.T) {
	fx := newWorkspaceManagerFixture(t)

	createInvite := func(userID pgtype.UUID, isAdmin bool, body string) (int, createOrganizationInviteResponse) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("organizationID", fx.orgID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = withPrincipal(ctx, fx.principal(userID, isAdmin))
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		fx.app.handleCreateOrganizationInvite(rr, req)
		var response createOrganizationInviteResponse
		if rr.Code == http.StatusCreated {
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode invite: %v body=%s", err, rr.Body.String())
			}
		}
		return rr.Code, response
	}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	if code, created := createInvite(fx.managerID, false, `{"expires_at":"`+expires+`","max_uses":5,"role":"manager"}`); code != http.StatusCreated || created.Invite.Role != "manager" {
		t.Fatalf("manager create manager-invite status = %d role=%q, want 201 manager", code, created.Invite.Role)
	}
	if code, created := createInvite(fx.ownerID, false, `{"expires_at":"`+expires+`","max_uses":5}`); code != http.StatusCreated || created.Invite.Role != "member" {
		t.Fatalf("owner create default invite status = %d role=%q, want 201 member", code, created.Invite.Role)
	}
	if code, _ := createInvite(fx.ownerID, false, `{"expires_at":"`+expires+`","max_uses":5,"role":"owner"}`); code != http.StatusBadRequest {
		t.Fatalf("owner-grant invite status = %d, want 400", code)
	}
	if code, _ := createInvite(fx.memberID, false, `{"expires_at":"`+expires+`","max_uses":5}`); code != http.StatusForbidden {
		t.Fatalf("member create invite status = %d, want 403", code)
	}

	acceptInvite := func(userID pgtype.UUID, token string) int {
		t.Helper()
		var sessionID pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO sessions (user_id, session_token_hash, supabase_access_token, supabase_refresh_token, supabase_access_token_expires_at, expires_at)
			VALUES ($1, $2, 'test', 'test', now() + interval '1 hour', now() + interval '1 hour') RETURNING id`, userID, fmt.Sprint("org-access-test-", time.Now().UnixNano())).Scan(&sessionID); err != nil {
			t.Fatalf("create session: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("token", token)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = internalauth.WithSession(ctx, internalauth.SessionContext{SessionID: sessionID, UserID: userID})
		ctx = withPrincipal(ctx, fx.principal(userID, false))
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		fx.app.handleAcceptInvite(rr, req)
		return rr.Code
	}
	memberRole := func(userID pgtype.UUID) string {
		t.Helper()
		membership, err := fx.app.Queries.GetOrganizationMember(fx.ctx, sqlc.GetOrganizationMemberParams{OrgID: fx.orgID, UserID: userID})
		if err != nil {
			t.Fatalf("read membership: %v", err)
		}
		return membership.Role
	}
	newUser := func(tag string) pgtype.UUID {
		t.Helper()
		var userID pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO users (auth_provider, auth_subject, email)
			VALUES ('org-access-test', $1, $2) RETURNING id`, "redeem-"+tag+fmt.Sprint(time.Now().UnixNano()), "redeem-"+tag+fmt.Sprint(time.Now().UnixNano())+"@example.com").Scan(&userID); err != nil {
			t.Fatalf("create redeem user: %v", err)
		}
		t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
		return userID
	}

	// Redeem installs the saved manager role for a new member.
	_, redeemable := createInvite(fx.ownerID, false, `{"expires_at":"`+expires+`","max_uses":5,"role":"manager"}`)
	if redeemable.Token == "" {
		t.Fatal("expected a raw token for the redeem flow")
	}
	newcomer := newUser("newcomer")
	if code := acceptInvite(newcomer, redeemable.Token); code != http.StatusOK {
		t.Fatalf("newcomer accept manager invite status = %d, want 200", code)
	}
	if role := memberRole(newcomer); role != "manager" {
		t.Fatalf("redeemed membership role = %q, want manager", role)
	}

	// Repeat redeems never downgrade: the newcomer, the owner, and the
	// manager keep their roles.
	if code := acceptInvite(newcomer, redeemable.Token); code != http.StatusConflict {
		t.Fatalf("repeat accept status = %d, want 409", code)
	}
	if role := memberRole(newcomer); role != "manager" {
		t.Fatalf("membership after repeat accept = %q, want manager", role)
	}
	if code := acceptInvite(fx.ownerID, redeemable.Token); code != http.StatusConflict {
		t.Fatalf("owner redeem status = %d, want 409", code)
	}
	if role := memberRole(fx.ownerID); role != "owner" {
		t.Fatalf("owner role after redeem = %q, want owner", role)
	}
	if code := acceptInvite(fx.managerID, redeemable.Token); code != http.StatusConflict {
		t.Fatalf("manager redeem status = %d, want 409", code)
	}
	if role := memberRole(fx.managerID); role != "manager" {
		t.Fatalf("manager role after redeem = %q, want manager", role)
	}
}
