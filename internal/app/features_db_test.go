package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The enabled default lives in SQL (LEFT JOIN + COALESCE), so it is verified
// against a real Postgres.
func newFeaturesTestQueries(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()

	if _, currentFilePath, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
		_ = godotenv.Load(filepath.Join(repoRoot, ".env"))
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)

	return sqlc.New(pool), pool, ctx
}

func createFeaturesTestOrg(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	var orgID pgtype.UUID
	err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ('features-test-org') RETURNING id`).Scan(&orgID)
	if err != nil {
		t.Fatalf("create test organization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

// A workspace with no organization_features row must resolve to all-enabled.
func TestOrgWithNoRowResolvesToEverythingEnabled(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}

	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.Integrations, row.AiUseInternalPrompt, row.AiMonthlyMessageLimit, row.AiConcurrentTurnLimitPerUser, row.MaxCompetitors, row.MaxProjects, row.AiAllowedReasoningEfforts)
	for _, feature := range []Feature{FeatureAutoCrawl, FeatureGSCConnector, FeatureAIChat, FeatureIntegrations, FeatureCompetitors} {
		if !features.Enabled(feature) {
			t.Errorf("unrestricted workspace has %q disabled", feature)
		}
	}
	if row.AiUseInternalPrompt {
		t.Error("workspace without a feature row resolved internal prompt enabled")
	}
	if row.AiMonthlyMessageLimit != 50 {
		t.Errorf("unrestricted workspace limit = %d, want 50", row.AiMonthlyMessageLimit)
	}
	if row.MaxCompetitors != 3 {
		t.Errorf("unrestricted workspace max_competitors = %d, want 3", row.MaxCompetitors)
	}
	if row.MaxProjects != 5 {
		t.Errorf("unrestricted workspace max_projects = %d, want 5", row.MaxProjects)
	}
	if !slices.Equal(row.AiAllowedReasoningEfforts, canonicalAIReasoningEfforts) {
		t.Errorf("unrestricted workspace efforts = %v, want %v", row.AiAllowedReasoningEfforts, canonicalAIReasoningEfforts)
	}
}

func TestUpsertThenReadRoundTrips(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	if err := queries.UpsertOrganizationFeatures(ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID:                         orgID,
		AutoCrawl:                     false,
		GscConnector:                  true,
		AiChat:                        true,
		Integrations:                  false,
		AiUseInternalPrompt:           true,
		AiMonthlyMessageLimit:         123,
		AiConcurrentTurnLimitPerUser:  2,
		AiVisibilityAuditMonthlyLimit: 10,
		MaxCompetitors:                3,
		MaxProjects:                   5,
		AiAllowedReasoningEfforts:     []string{"none", "high"},
	}); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.Integrations, row.AiUseInternalPrompt, row.AiMonthlyMessageLimit, row.AiConcurrentTurnLimitPerUser, row.MaxCompetitors, row.MaxProjects, row.AiAllowedReasoningEfforts)

	if features.Enabled(FeatureAutoCrawl) {
		t.Error("auto_crawl was saved disabled but read back enabled")
	}
	if !features.Enabled(FeatureGSCConnector) {
		t.Error("gsc_connector was saved enabled but read back disabled")
	}
	if features.Enabled(FeatureIntegrations) {
		t.Error("integrations was saved disabled but read back enabled")
	}
	if !features.AIUseInternalPrompt {
		t.Error("ai_use_internal_prompt was saved true but read back false")
	}
	if row.AiMonthlyMessageLimit != 123 {
		t.Errorf("limit = %d, want 123", row.AiMonthlyMessageLimit)
	}
	if !slices.Equal(row.AiAllowedReasoningEfforts, []string{"none", "high"}) {
		t.Errorf("efforts = %v, want [none high]", row.AiAllowedReasoningEfforts)
	}
}

// Saving twice must update in place rather than erroring on the primary key,
// since the admin page re-saves the whole matrix on every click of Save.
func TestUpsertIsIdempotentAndOverwrites(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	first := sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: false, GscConnector: false, AiChat: false, Integrations: false,
		AiMonthlyMessageLimit: 1, AiConcurrentTurnLimitPerUser: 2,
		AiVisibilityAuditMonthlyLimit: 10, MaxCompetitors: 3, MaxProjects: 5,
		AiAllowedReasoningEfforts: []string{"max"},
	}
	if err := queries.UpsertOrganizationFeatures(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	second := sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: true, GscConnector: true, AiChat: true, Integrations: true,
		AiMonthlyMessageLimit: 999, AiConcurrentTurnLimitPerUser: 2,
		AiVisibilityAuditMonthlyLimit: 10, MaxCompetitors: 3, MaxProjects: 5,
		AiAllowedReasoningEfforts: []string{"low", "none"},
	}
	if err := queries.UpsertOrganizationFeatures(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.Integrations, row.AiUseInternalPrompt, row.AiMonthlyMessageLimit, row.AiConcurrentTurnLimitPerUser, row.MaxCompetitors, row.MaxProjects, row.AiAllowedReasoningEfforts)

	if !features.Enabled(FeatureAutoCrawl) || !features.Enabled(FeatureGSCConnector) || !features.Enabled(FeatureAIChat) || !features.Enabled(FeatureIntegrations) {
		t.Error("re-enabling via a second save did not take effect")
	}
	if row.AiMonthlyMessageLimit != 999 || !slices.Equal(row.AiAllowedReasoningEfforts, []string{"none", "low"}) {
		t.Errorf("settings did not round-trip: limit=%d efforts=%v", row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts)
	}
}

// The admin matrix must list every workspace, including ones never restricted —
// otherwise a workspace could not be gated for the first time.
func TestAdminListIncludesUnrestrictedWorkspaces(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	rows, err := queries.ListOrganizationFeaturesForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListOrganizationFeaturesForAdmin: %v", err)
	}

	for _, row := range rows {
		if row.OrgID != orgID {
			continue
		}
		if !row.AutoCrawl || !row.GscConnector || !row.AiChat || !row.Integrations {
			t.Error("an unrestricted workspace is listed with features disabled")
		}
		if row.AiMonthlyMessageLimit != 50 || !slices.Equal(row.AiAllowedReasoningEfforts, canonicalAIReasoningEfforts) {
			t.Errorf("unrestricted settings = limit %d efforts %v", row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts)
		}
		return
	}
	t.Fatal("the unrestricted test workspace is missing from the admin matrix")
}

// featureCapTestApp wires a real DB, an org with an owner, and a project
// creation request builder. Skipped when no test database is available.
func featureCapTestApp(t *testing.T) (*App, *sqlc.Queries, pgtype.UUID, pgtype.UUID) {
	t.Helper()

	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	name := fmt.Sprintf("project-cap-test-%d", time.Now().UnixNano())
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}

	return &App{DB: pool, Queries: queries}, queries, orgID, userID
}

// projectCapRequest carries the organization id in the chi route context and
// the caller identity the way the authenticated route stack provides them. The
// base_url is a public IP literal so ValidatePublicHost never needs DNS.
func projectCapRequest(t *testing.T, orgID, userID pgtype.UUID, name string) *http.Request {
	t.Helper()

	body := fmt.Sprintf(`{"name":%q,"base_url":"https://93.184.216.34"}`, name)
	request := httptest.NewRequest(http.MethodPost, "/organizations/"+orgID.String()+"/projects", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("organizationID", orgID.String())
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return request.WithContext(ctx)
}

// capParams builds a full upsert payload; max_projects is the only field under
// test, but every column is supplied so the upsert overwrites deterministically.
func capParams(orgID pgtype.UUID, maxProjects int32) sqlc.UpsertOrganizationFeaturesParams {
	return sqlc.UpsertOrganizationFeaturesParams{
		OrgID:                         orgID,
		AutoCrawl:                     true,
		GscConnector:                  true,
		AiChat:                        true,
		Integrations:                  true,
		AiMonthlyMessageLimit:         50,
		AiConcurrentTurnLimitPerUser:  2,
		AiVisibilityAuditMonthlyLimit: 10,
		MaxCompetitors:                3,
		MaxProjects:                   maxProjects,
		AiAllowedReasoningEfforts:     canonicalAIReasoningEfforts,
	}
}

// A stored non-default cap must survive the round trip, on both the insert and
// the ON CONFLICT update path. Re-declaring the default value would pass even if
// the upsert dropped max_projects on the floor.
func TestMaxProjectsRoundTripsNonDefaultValue(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	if err := queries.UpsertOrganizationFeatures(ctx, capParams(orgID, 7)); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}
	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	if row.MaxProjects != 7 {
		t.Errorf("max_projects after insert = %d, want 7", row.MaxProjects)
	}

	if err := queries.UpsertOrganizationFeatures(ctx, capParams(orgID, 9)); err != nil {
		t.Fatalf("UpsertOrganizationFeatures (conflict path): %v", err)
	}
	row, err = queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	if row.MaxProjects != 9 {
		t.Errorf("max_projects after update = %d, want 9", row.MaxProjects)
	}
}

// The cap must refuse new projects once the workspace is at the limit, while
// leaving a workspace that is already over the limit intact.
func TestCreateProjectEnforcesWorkspaceProjectLimit(t *testing.T) {
	app, queries, orgID, userID := featureCapTestApp(t)
	ctx := context.Background()

	if err := queries.UpsertOrganizationFeatures(ctx, capParams(orgID, 2)); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}

	for _, name := range []string{"cap-first", "cap-second"} {
		recorder := httptest.NewRecorder()
		app.handleCreateProject(recorder, projectCapRequest(t, orgID, userID, name))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create %s: status = %d, want 201; body = %s", name, recorder.Code, recorder.Body.String())
		}
	}

	recorder := httptest.NewRecorder()
	app.handleCreateProject(recorder, projectCapRequest(t, orgID, userID, "cap-third"))
	if recorder.Code != http.StatusConflict || recorder.Body.String() != "{\"error\":\"project_limit_reached\"}\n" {
		t.Fatalf("third create = %d %q, want 409 project_limit_reached", recorder.Code, recorder.Body.String())
	}

	count, err := queries.CountProjectsForOrganization(ctx, orgID)
	if err != nil {
		t.Fatalf("CountProjectsForOrganization: %v", err)
	}
	if count != 2 {
		t.Fatalf("project count = %d, want 2 (the refused create must not persist)", count)
	}

	// Lowering the cap below the current count must not delete existing projects;
	// it only blocks the next create.
	if err := queries.UpsertOrganizationFeatures(ctx, capParams(orgID, 1)); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}
	recorder = httptest.NewRecorder()
	app.handleCreateProject(recorder, projectCapRequest(t, orgID, userID, "cap-fourth"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("over-limit create = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
	}

	projects, err := queries.ListProjectsForOrganization(ctx, orgID)
	if err != nil {
		t.Fatalf("ListProjectsForOrganization: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("over-limit workspace has %d projects, want the original 2 preserved", len(projects))
	}
}
