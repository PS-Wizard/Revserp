package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Scratch-database integration checks for the location workspace handlers that
// need a real migrated schema. The URL comes from LOCATION_SCRATCH_TEST_DATABASE_URL
// only; the suite refuses any non-local host or non-scratch database name.
const scratchAppDatabaseEnv = "LOCATION_SCRATCH_TEST_DATABASE_URL"

func scratchAppFixture(t *testing.T) (*App, *pgxpool.Pool, context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	raw := os.Getenv(scratchAppDatabaseEnv)
	if raw == "" {
		t.Skip(scratchAppDatabaseEnv + " is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", scratchAppDatabaseEnv, err)
	}
	if name := strings.Trim(parsed.Path, "/"); !strings.HasPrefix(name, "revserp_layer_c_") {
		t.Fatalf("refusing %s database %q: want a revserp_layer_c_* scratch database", scratchAppDatabaseEnv, name)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("refusing %s host %q: want 127.0.0.1 or localhost", scratchAppDatabaseEnv, host)
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, raw, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("%s is not available: %v", scratchAppDatabaseEnv, err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("scratchapp-%d-%d", os.Getpid(), time.Now().UnixNano())
	var orgID, userID, projectID, locationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, prefix).Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test',$1,$2) RETURNING id`,
		prefix, prefix+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add owner member: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,$2,'https://scratch.example') RETURNING id`,
		orgID, prefix).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,$2,27.7172,85.3240) RETURNING id`,
		projectID, prefix+"-loc").Scan(&locationID); err != nil {
		t.Fatalf("create location: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		conn, err := pool.Acquire(bg)
		if err != nil {
			return
		}
		defer conn.Release()
		// The migration 091 unsettled-spend guard and the migration 100 scope
		// trigger block the cascades, so they are disabled only inside this
		// cleanup transaction. No shared account is deleted explicitly.
		if _, err := conn.Exec(bg, `BEGIN`); err != nil {
			return
		}
		_, _ = conn.Exec(bg, `ALTER TABLE project_locations DISABLE TRIGGER location_unsettled_maps_spend_guard`)
		_, _ = conn.Exec(bg, `ALTER TABLE location_website_scopes DISABLE TRIGGER location_website_scope_revision_immutable`)
		_, _ = conn.Exec(bg, `DELETE FROM ai_conversations WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1)`, orgID)
		// Bindings before the org: the 104 account FKs must not block cleanup.
		_, _ = conn.Exec(bg, `DELETE FROM location_gsc_connections WHERE location_id IN (SELECT id FROM project_locations WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1))`, orgID)
		_, _ = conn.Exec(bg, `DELETE FROM location_google_analytics_connections WHERE location_id IN (SELECT id FROM project_locations WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1))`, orgID)
		_, _ = conn.Exec(bg, `DELETE FROM project_gsc_connections WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1)`, orgID)
		_, _ = conn.Exec(bg, `DELETE FROM project_google_analytics_connections WHERE project_id IN (SELECT id FROM projects WHERE organization_id=$1)`, orgID)
		_, _ = conn.Exec(bg, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = conn.Exec(bg, `ALTER TABLE location_website_scopes ENABLE TRIGGER location_website_scope_revision_immutable`)
		_, _ = conn.Exec(bg, `ALTER TABLE project_locations ENABLE TRIGGER location_unsettled_maps_spend_guard`)
		_, _ = conn.Exec(bg, `COMMIT`)
		_, _ = conn.Exec(bg, `DELETE FROM users WHERE id=$1`, userID)
	})
	return &App{DB: pool, Queries: sqlc.New(pool)}, pool, ctx, orgID, userID, projectID, locationID
}

// Terminal location chat history is removed with the location, while a
// conversation with a live turn still blocks deletion with a 409 and keeps
// both the chat and the location.
func TestScratchDeleteLocationWithConversationHistory(t *testing.T) {
	app, pool, ctx, _, userID, projectID, locationID := scratchAppFixture(t)
	var conversationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_conversations (project_id, created_by_user_id, title, location_id) VALUES ($1,$2,'scratch chat',$3) RETURNING id`,
		projectID, userID, locationID).Scan(&conversationID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	var turnID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_turns (conversation_id, created_by_user_id, status, requested_effort, effective_effort, model, prompt_version, client_request_id, request_hash)
		VALUES ($1,$2,'completed','none','none','m','v','scratch',decode(repeat('ab',32),'hex')) RETURNING id`,
		conversationID, userID).Scan(&turnID); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_messages (turn_id, role, status, content) VALUES ($1,'user','complete','hi'),($1,'assistant','complete','hello')`, turnID); err != nil {
		t.Fatalf("create messages: %v", err)
	}

	rr := callDeleteProjectLocation(t, app, userID, projectID.String(), locationID.String())
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete with terminal chat = %d: %s", rr.Code, rr.Body.String())
	}
	var conversations, turns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_conversations WHERE id=$1`, conversationID).Scan(&conversations); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turns WHERE id=$1`, turnID).Scan(&turns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if conversations != 0 || turns != 0 {
		t.Fatalf("terminal chat survived: conversations=%d turns=%d", conversations, turns)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM project_locations WHERE id=$1`, locationID); err != nil {
		t.Fatalf("recreate check: %v", err)
	}
}

// A live Revbot turn blocks permanent location deletion with a 409 and the
// composite FK still blocks a direct location delete while chat exists.
func TestScratchDeleteLocationWithLiveTurnReturnsConflict(t *testing.T) {
	app, pool, ctx, _, userID, projectID, locationID := scratchAppFixture(t)
	var conversationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_conversations (project_id, created_by_user_id, title, location_id) VALUES ($1,$2,'scratch chat',$3) RETURNING id`,
		projectID, userID, locationID).Scan(&conversationID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_turns (conversation_id, created_by_user_id, status, requested_effort, effective_effort, model, prompt_version, client_request_id, request_hash)
		VALUES ($1,$2,'running','none','none','m','v','scratch-live',decode(repeat('cd',32),'hex'))`, conversationID, userID); err != nil {
		t.Fatalf("create live turn: %v", err)
	}

	rr := callDeleteProjectLocation(t, app, userID, projectID.String(), locationID.String())
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "Revbot conversation") {
		t.Fatalf("delete with live turn = %d: %s", rr.Code, rr.Body.String())
	}
	var conversations, locations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_conversations WHERE id=$1`, conversationID).Scan(&conversations); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_locations WHERE id=$1`, locationID).Scan(&locations); err != nil {
		t.Fatalf("count locations: %v", err)
	}
	if conversations != 1 || locations != 1 {
		t.Fatalf("conflict changed rows: conversations=%d locations=%d", conversations, locations)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM project_locations WHERE id=$1`, locationID); err == nil {
		t.Fatal("the composite FK must block a direct location delete while chat exists")
	}
}

func enabledMapQueryKeys(t *testing.T, ctx context.Context, pool *pgxpool.Pool, locationID pgtype.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT normalized FROM project_location_queries WHERE location_id=$1 AND kind='map' AND enabled ORDER BY normalized`, locationID)
	if err != nil {
		t.Fatalf("list enabled map queries: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan key: %v", err)
		}
		keys = append(keys, key)
	}
	return keys
}

func mapQueryRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, locationID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_location_queries WHERE location_id=$1 AND kind='map'`, locationID).Scan(&count); err != nil {
		t.Fatalf("count map rows: %v", err)
	}
	return count
}

// After PUT keyword-lists the enabled Maps draft must be exactly the selected
// set, including disabling every row when the selection is empty, without
// deleting draft rows or touching frozen run snapshots.
func TestScratchKeywordPutSyncsEnabledMapQueries(t *testing.T) {
	app, pool, ctx, _, userID, projectID, locationID := scratchAppFixture(t)
	seed := func(text, normalized string, enabled bool, ordinal int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin)
			VALUES ($1,$2,$3,$4,$5,'map','manual','locality')`, locationID, text, normalized, ordinal, enabled); err != nil {
			t.Fatalf("seed draft %q: %v", text, err)
		}
	}
	seed("Acme", "acme", true, 0)
	seed("Plumber", "plumber", true, 1)
	seed("Dormant", "dormant", false, 2)

	var runID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits)
		VALUES ($1,'completed',5000,'{"queries":["Acme","Plumber"]}'::jsonb,2,0) RETURNING id`, locationID).Scan(&runID); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := localVisibilityRequest(t, http.MethodPut, userID, map[string]string{"projectID": projectID.String(), "locationID": locationID.String()}, body)
		rr := httptest.NewRecorder()
		app.handlePutLocationKeywordLists(rr, req)
		return rr
	}

	rr := put(`{"user_defined":{"branded":[],"non_branded":[]},"selected":{"branded":["Acme"],"non_branded":["Leak Repair"]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT selected = %d: %s", rr.Code, rr.Body.String())
	}
	got := enabledMapQueryKeys(t, ctx, pool, locationID)
	if strings.Join(got, ",") != "acme,leak repair" {
		t.Fatalf("enabled after PUT = %v, want exactly the selected set [acme leak repair]", got)
	}
	if rows := mapQueryRowCount(t, ctx, pool, locationID); rows != 4 {
		t.Fatalf("map draft rows = %d, want 4 (deselect disables, never deletes)", rows)
	}

	rr = put(`{"user_defined":{"branded":[],"non_branded":[]},"selected":{"branded":[],"non_branded":[]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT empty selected = %d: %s", rr.Code, rr.Body.String())
	}
	if got := enabledMapQueryKeys(t, ctx, pool, locationID); len(got) != 0 {
		t.Fatalf("enabled after empty PUT = %v, want none", got)
	}
	if rows := mapQueryRowCount(t, ctx, pool, locationID); rows != 4 {
		t.Fatalf("map draft rows after empty PUT = %d, want 4 (rows kept, disabled)", rows)
	}

	var snapshot string
	if err := pool.QueryRow(ctx, `SELECT snapshot::text FROM local_visibility_runs WHERE id=$1`, runID).Scan(&snapshot); err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if snapshot != `{"queries": ["Acme", "Plumber"]}` {
		t.Fatalf("frozen run snapshot changed to %s", snapshot)
	}
}

// 091 credit protection is intentional: deleting a location with a queued or
// unconfirmed Maps run returns 409 and keeps the row until the run is settled.
func TestScratchDeleteLocationWithUnsettledSpendReturnsConflict(t *testing.T) {
	app, pool, ctx, _, userID, projectID, locationID := scratchAppFixture(t)
	if _, err := pool.Exec(ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits)
		VALUES ($1,'queued',5000,'{}'::jsonb,1,1)`, locationID); err != nil {
		t.Fatalf("queued run: %v", err)
	}
	rr := callDeleteProjectLocation(t, app, userID, projectID.String(), locationID.String())
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "settle active or unconfirmed spending") {
		t.Fatalf("delete with unsettled spend = %d: %s", rr.Code, rr.Body.String())
	}
	var locations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_locations WHERE id=$1`, locationID).Scan(&locations); err != nil {
		t.Fatalf("count locations: %v", err)
	}
	if locations != 1 {
		t.Fatal("unsettled location must be retained")
	}
	if _, err := pool.Exec(ctx, `UPDATE local_visibility_runs SET status='completed', reserved_credits=0, credits_used=1 WHERE location_id=$1`, locationID); err != nil {
		t.Fatalf("settle run: %v", err)
	}
	rr = callDeleteProjectLocation(t, app, userID, projectID.String(), locationID.String())
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete after settle = %d: %s", rr.Code, rr.Body.String())
	}
}

// The saved radius_m is passed through by both location DTO callers without an
// extra query: GET location and GET workspace must echo the stored 12500.
func TestScratchRadiusMPassthroughFromSavedRow(t *testing.T) {
	app, pool, ctx, _, userID, projectID, locationID := scratchAppFixture(t)
	if _, err := pool.Exec(ctx, `UPDATE project_locations SET radius_m=12500 WHERE id=$1`, locationID); err != nil {
		t.Fatalf("update radius: %v", err)
	}
	params := map[string]string{"projectID": projectID.String(), "locationID": locationID.String()}

	rr := httptest.NewRecorder()
	app.handleGetProjectLocation(rr, localVisibilityRequest(t, http.MethodGet, userID, params, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET location = %d: %s", rr.Code, rr.Body.String())
	}
	var location localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &location); err != nil {
		t.Fatalf("decode location: %v", err)
	}
	if location.RadiusM != 12500 {
		t.Fatalf("GET location radius_m = %d, want 12500", location.RadiusM)
	}

	rr = httptest.NewRecorder()
	app.handleGetLocationWorkspace(rr, localVisibilityRequest(t, http.MethodGet, userID, params, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET workspace = %d: %s", rr.Code, rr.Body.String())
	}
	var workspace locationWorkspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &workspace); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if workspace.Location.RadiusM != 12500 {
		t.Fatalf("GET workspace location radius_m = %d, want 12500", workspace.Location.RadiusM)
	}
}

// The location AI question endpoints persist an independent set, enqueue one
// explicit location-scoped job, and keep duplicate clicks idempotent.
func TestScratchLocationAIQuestionsEndpoints(t *testing.T) {
	app, pool, ctx, _, userID, projectID, locationID := scratchAppFixture(t)
	if _, err := pool.Exec(ctx, `INSERT INTO location_business_profiles (project_id, location_id, brand_name, website_url)
		VALUES ($1,$2,'Acme Downtown','https://acme.example')`, projectID, locationID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	params := map[string]string{"projectID": projectID.String(), "locationID": locationID.String()}
	call := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		req := localVisibilityRequest(t, method, userID, params, body)
		switch method {
		case http.MethodGet:
			app.handleGetLocationAIQuestions(rr, req)
		case http.MethodPut:
			app.handlePutLocationAIQuestions(rr, req)
		case http.MethodPost:
			app.handleRegenerateLocationAIQuestions(rr, req)
		}
		return rr
	}

	rr := call(http.MethodGet, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET empty = %d: %s", rr.Code, rr.Body.String())
	}
	var empty locationAIQuestionsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if len(empty.Questions) != 0 {
		t.Fatalf("empty GET questions = %v, want none", empty.Questions)
	}

	rr = call(http.MethodPut, `{"questions":["best downtown dentist?","Best Downtown Dentist?","where to get braces?"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}
	var saved locationAIQuestionsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if len(saved.Questions) != 2 || saved.Questions[0] != "best downtown dentist?" {
		t.Fatalf("saved questions = %v, want deduped 2", saved.Questions)
	}

	rr = call(http.MethodGet, "")
	if err := json.Unmarshal(rr.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode reread: %v", err)
	}
	if len(empty.Questions) != 2 {
		t.Fatalf("reread questions = %v, want 2", empty.Questions)
	}

	rr = call(http.MethodPost, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("regenerate = %d: %s", rr.Code, rr.Body.String())
	}
	rr = call(http.MethodPost, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("second regenerate = %d: %s", rr.Code, rr.Body.String())
	}
	var active int
	countActive := func() int {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_worker_jobs WHERE project_id=$1 AND location_id=$2 AND job_type='prompt_generation' AND status IN ('pending','running')`, projectID, locationID).Scan(&active); err != nil {
			t.Fatalf("count active: %v", err)
		}
		return active
	}
	if got := countActive(); got != 1 {
		t.Fatalf("active location jobs = %d, want 1 (idempotent pending)", got)
	}
	// A claimed (running) job must still block a second paid enqueue.
	if _, err := pool.Exec(ctx, `UPDATE ai_worker_jobs SET status='running' WHERE project_id=$1 AND location_id=$2 AND status='pending'`, projectID, locationID); err != nil {
		t.Fatalf("claim job: %v", err)
	}
	rr = call(http.MethodPost, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("regenerate while running = %d: %s", rr.Code, rr.Body.String())
	}
	if got := countActive(); got != 1 {
		t.Fatalf("active location jobs = %d, want 1 while running", got)
	}
	// A completed job is not active, so a fresh regeneration is allowed.
	if _, err := pool.Exec(ctx, `UPDATE ai_worker_jobs SET status='completed' WHERE project_id=$1 AND location_id=$2`, projectID, locationID); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	rr = call(http.MethodPost, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("regenerate after completed = %d: %s", rr.Code, rr.Body.String())
	}
	if got := countActive(); got != 1 {
		t.Fatalf("active location jobs = %d, want 1 after completed", got)
	}
}
