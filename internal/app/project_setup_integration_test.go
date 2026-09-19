package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// requireProjectSetupMigration skips instead of failing when migration 000072
// has not been applied to the test database (or an older revision of it is).
func requireProjectSetupMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_name = 'project_setup'
		  AND column_name = 'failed_step'
	`).Scan(&columns); err != nil {
		t.Fatalf("check project_setup schema: %v", err)
	}
	if columns == 0 {
		t.Skip("project_setup is not migrated to the current schema")
	}
}

func newProjectSetupTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID pgtype.UUID, role string) pgtype.UUID {
	t.Helper()
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('project-setup-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	if role != "" {
		if _, err := pool.Exec(ctx,
			`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, $3)`,
			orgID, userID, role); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}
	return userID
}

func callProjectSetup(t *testing.T, app *App, userID, projectID pgtype.UUID, method string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("projectID", projectID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	if method == http.MethodPost {
		app.handleStartProjectSetup(rr, req)
	} else {
		app.handleGetProjectSetup(rr, req)
	}
	return rr
}

func decodeProjectSetup(t *testing.T, rr *httptest.ResponseRecorder) projectSetupResponse {
	t.Helper()
	var body projectSetupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	return body
}

func TestProjectSetupWorkflowIntegration(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	requireProjectSetupMigration(t, ctx, pool)
	orgID := createFeaturesTestOrg(t, ctx, pool)
	app := &App{DB: pool, Queries: queries}

	owner := newProjectSetupTestUser(t, ctx, pool, orgID, "owner")
	member := newProjectSetupTestUser(t, ctx, pool, orgID, "member")
	outsider := newProjectSetupTestUser(t, ctx, pool, pgtype.UUID{}, "")

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'setup-test', 'https://setup.example')
		RETURNING id
	`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if rr := callProjectSetup(t, app, owner, projectID, http.MethodGet); rr.Code != http.StatusNotFound {
		t.Fatalf("GET before setup status = %d, want 404 (body=%s)", rr.Code, rr.Body.String())
	}

	if rr := callProjectSetup(t, app, outsider, projectID, http.MethodGet); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider GET status = %d, want 404", rr.Code)
	}

	if rr := callProjectSetup(t, app, member, projectID, http.MethodPost); rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner POST status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
	if count := countProjectCrawls(t, ctx, pool, projectID); count != 0 {
		t.Fatalf("non-owner POST created %d crawls, want 0", count)
	}

	// A project with no setup row is legacy: POST must not create one and
	// must leave the project on manual flows.
	if rr := callProjectSetup(t, app, owner, projectID, http.MethodPost); rr.Code != http.StatusNotFound {
		t.Fatalf("legacy POST status = %d, want 404 (body=%s)", rr.Code, rr.Body.String())
	}
	if count := countProjectCrawls(t, ctx, pool, projectID); count != 0 {
		t.Fatalf("legacy POST created %d crawls, want 0", count)
	}
	if count := countProjectSetups(t, ctx, pool, projectID); count != 0 {
		t.Fatalf("legacy POST created %d setup rows, want 0", count)
	}

	// Project creation seeds the setup row in 'ready'; that insert must not
	// emit a setup event.
	var setupID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO project_setup (organization_id, project_id, requested_by_user_id, status)
		VALUES ($1, $2, $3, 'ready')
		RETURNING id
	`, orgID, projectID, owner).Scan(&setupID); err != nil {
		t.Fatalf("seed ready setup: %v", err)
	}
	if events := latestProjectSetupEventTypes(t, ctx, pool, orgID, setupID); len(events) != 0 {
		t.Fatalf("ready insert emitted events %v, want none", events)
	}

	rr := callProjectSetup(t, app, owner, projectID, http.MethodPost)
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner POST status = %d, want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	created := decodeProjectSetup(t, rr)
	if created.ID == "" || created.Status != projectSetupStatusCrawling {
		t.Fatalf("started setup = %+v, want status crawling", created)
	}
	if created.ID != setupID.String() {
		t.Fatalf("started setup id = %s, want seeded %s", created.ID, setupID.String())
	}
	if created.ProjectID != projectID.String() || created.OrganizationID != orgID.String() {
		t.Fatalf("created setup ids = %+v, want project %s org %s", created, projectID.String(), orgID.String())
	}
	if created.CrawlID == "" {
		t.Fatal("created setup has no crawl_id")
	}
	if count := countProjectCrawls(t, ctx, pool, projectID); count != 1 {
		t.Fatalf("owner POST crawl count = %d, want 1", count)
	}

	// Re-click must return the same row and must not enqueue a second crawl.
	rr = callProjectSetup(t, app, owner, projectID, http.MethodPost)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-click POST status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	reclicked := decodeProjectSetup(t, rr)
	if reclicked.ID != created.ID {
		t.Fatalf("re-click id = %s, want %s", reclicked.ID, created.ID)
	}
	if count := countProjectCrawls(t, ctx, pool, projectID); count != 1 {
		t.Fatalf("re-click crawl count = %d, want 1", count)
	}
	if count := countProjectSetups(t, ctx, pool, projectID); count != 1 {
		t.Fatalf("setup row count = %d, want 1", count)
	}

	rr = callProjectSetup(t, app, member, projectID, http.MethodGet)
	if rr.Code != http.StatusOK {
		t.Fatalf("member GET status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if got := decodeProjectSetup(t, rr); got.ID != created.ID || got.CrawlID != created.CrawlID {
		t.Fatalf("member GET = %+v, want id %s crawl %s", got, created.ID, created.CrawlID)
	}

	// The insert trigger must have emitted the start event.
	if got := latestProjectSetupEventTypes(t, ctx, pool, orgID, mustUUID(t, created.ID)); len(got) == 0 || got[0] != "project_setup.started" {
		t.Fatalf("setup events = %v, want first project_setup.started", got)
	}

	// Worker-side transitions advance the durable row and emit state events.
	if _, err := queries.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		ProjectID:      projectID,
		Status:         projectSetupStatusProfileGeneration,
		ExpectedStatus: projectSetupStatusCrawling,
	}); err != nil {
		t.Fatalf("advance to profile_generation: %v", err)
	}
	if _, err := queries.UpdateProjectSetupStatus(ctx, sqlc.UpdateProjectSetupStatusParams{
		ProjectID:      projectID,
		Status:         projectSetupStatusFailed,
		ExpectedStatus: projectSetupStatusProfileGeneration,
		FailedStep:     pgtype.Text{String: projectSetupStatusPromptGeneration, Valid: true},
	}); err != nil {
		t.Fatalf("advance to failed: %v", err)
	}

	row, err := queries.GetProjectSetupByProjectID(ctx, projectID)
	if err != nil {
		t.Fatalf("get setup: %v", err)
	}
	if row.Status != projectSetupStatusFailed || !row.CompletedAt.Valid {
		t.Fatalf("row after failure = %+v, want failed with completed_at", row)
	}
	if !row.FailedStep.Valid || row.FailedStep.String != projectSetupStatusPromptGeneration {
		t.Fatalf("row failed_step = %+v, want prompt_generation", row.FailedStep)
	}
	events := latestProjectSetupEventTypes(t, ctx, pool, orgID, mustUUID(t, created.ID))
	want := []string{"project_setup.started", "project_setup.status_changed", "project_setup.failed"}
	if len(events) != len(want) {
		t.Fatalf("setup events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("setup events = %v, want %v", events, want)
		}
	}
}

func countProjectCrawls(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM crawls WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatalf("count crawls: %v", err)
	}
	return count
}

func countProjectSetups(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_setup WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatalf("count setup rows: %v", err)
	}
	return count
}

func latestProjectSetupEventTypes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, setupID pgtype.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT event_type
		FROM organization_events
		WHERE organization_id = $1
		  AND resource_id = $2
		ORDER BY id ASC
	`, orgID, setupID)
	if err != nil {
		t.Fatalf("list setup events: %v", err)
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan setup event: %v", err)
		}
		events = append(events, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate setup events: %v", err)
	}
	return events
}
