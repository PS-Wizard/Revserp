package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

// Local visibility handler tests. Validation tests need no database. The
// cross-organization and run tests connect only to the disposable database
// named by LOCAL_SEO_TEST_DATABASE_URL and skip when it is absent.

func newLocalVisibilityTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("LOCAL_SEO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LOCAL_SEO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("local visibility test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"public.local_visibility_runs", "public.project_locations"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("table %s is not migrated in the test database", table)
		}
	}
	return sqlc.New(pool), pool, ctx
}

type localVisibilityFixture struct {
	app       *App
	pool      *pgxpool.Pool
	ctx       context.Context
	orgID     pgtype.UUID
	projectID pgtype.UUID
	ownerID   pgtype.UUID
	memberID  pgtype.UUID
	outsider  pgtype.UUID
}

func newLocalVisibilityFixture(t *testing.T) localVisibilityFixture {
	t.Helper()
	queries, pool, ctx := newLocalVisibilityTestPool(t)
	name := fmt.Sprintf("local-visibility-test-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
			VALUES ('local-visibility-test', $1, $2) RETURNING id`, name+role, name+role+"@example.com").Scan(&userID); err != nil {
			t.Fatalf("create user: %v", err)
		}
		if role != "outsider" {
			if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,$3)`, orgID, userID, role); err != nil {
				t.Fatalf("add member: %v", err)
			}
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
		return userID
	}
	ownerID := newUser("owner")
	memberID := newUser("member")
	outsider := newUser("outsider")
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'local-visibility-test','https://local-visibility.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// Delete only this fixture's lookups and runs, so the guard on
		// project_locations does not block teardown and no other org is touched.
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM local_listing_lookups WHERE location_id IN (SELECT l.id FROM project_locations l JOIN projects p ON p.id = l.project_id WHERE p.organization_id = $1)`, orgID)
		_, _ = pool.Exec(cleanupCtx, `UPDATE local_visibility_runs SET status = 'failed', reserved_credits = 0 WHERE location_id IN (SELECT id FROM project_locations WHERE project_id = $1)`, projectID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM projects WHERE id=$1`, projectID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id=$1`, orgID)
	})
	return localVisibilityFixture{
		app:       &App{DB: pool, Queries: queries, Config: config.Config{SerperMapsEndpoint: "http://127.0.0.1:1/local-seo-test/maps"}},
		pool:      pool,
		ctx:       ctx,
		orgID:     orgID,
		projectID: projectID,
		ownerID:   ownerID,
		memberID:  memberID,
		outsider:  outsider,
	}
}

// localVisibilityFakePlatformCredits is a large fake top-up applied to the
// shared platform singleton. Only this test's own deltas are reversed on
// cleanup; the singleton is also used by the manual UI, so the captured row
// must never be restored wholesale.
const localVisibilityFakePlatformCredits int64 = 1000000

// fundLocalVisibilityBudgets provisions the dedicated organization allowance
// and raises the shared platform cap by a fake amount.
func (fx localVisibilityFixture) fundLocalVisibilityBudgets(t *testing.T) {
	t.Helper()
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE platform_maps_credit_budget SET remaining_credits = remaining_credits + $1 WHERE id = TRUE`, localVisibilityFakePlatformCredits); err != nil {
		t.Fatalf("fund platform budget: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO organization_maps_credit_budgets (organization_id, remaining_credits)
		VALUES ($1, $2) ON CONFLICT (organization_id) DO UPDATE SET remaining_credits = EXCLUDED.remaining_credits, reserved_credits = 0, spent_credits = 0`, fx.orgID, localVisibilityFakePlatformCredits); err != nil {
		t.Fatalf("fund org budget: %v", err)
	}
	t.Cleanup(fx.releasePlatformDeltas)
}

// releasePlatformDeltas reverses only this fixture's contribution to the
// shared platform singleton. The dedicated org's spent/reserved totals are
// exactly this test's platform deltas, even while the manual UI uses the same
// platform row on unrelated organizations.
func (fx localVisibilityFixture) releasePlatformDeltas() {
	ctx := context.Background()
	var spent, reserved int64
	if err := fx.pool.QueryRow(ctx, `SELECT spent_credits, reserved_credits FROM organization_maps_credit_budgets WHERE organization_id = $1`, fx.orgID).Scan(&spent, &reserved); err != nil {
		return
	}
	_, _ = fx.pool.Exec(ctx, `UPDATE platform_maps_credit_budget
		SET remaining_credits = remaining_credits - $1 + $2,
		    reserved_credits = GREATEST(reserved_credits - $3, 0),
		    spent_credits = GREATEST(spent_credits - $2, 0)
		WHERE id = TRUE`, localVisibilityFakePlatformCredits, spent, reserved)
}

// exhaustOrganizationAllowance zeros only the dedicated test org's funds. It
// never touches the shared platform singleton.
func (fx localVisibilityFixture) exhaustOrganizationAllowance(t *testing.T) {
	t.Helper()
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE organization_maps_credit_budgets SET remaining_credits = 0 WHERE organization_id = $1`, fx.orgID); err != nil {
		t.Fatalf("exhaust organization allowance: %v", err)
	}
}

func localVisibilityRequest(t *testing.T, method string, userID pgtype.UUID, params map[string]string, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

func callCreateLocation(t *testing.T, app *App, userID pgtype.UUID, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID}, body)
	rr := httptest.NewRecorder()
	app.handleCreateProjectLocation(rr, req)
	return rr
}

func callGetLocation(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	app.handleGetProjectLocation(rr, req)
	return rr
}

func callUpdateLocationQueries(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPut, userID, map[string]string{"projectID": projectID, "locationID": locationID}, body)
	rr := httptest.NewRecorder()
	app.handleUpdateLocationQueries(rr, req)
	return rr
}

// layer4DraftJSON builds the bare ordered PUT array the draft endpoint owns:
// position is the ordinal and absence of id means a new manual row.
func layer4DraftJSON(texts ...string) string {
	entries := make([]string, 0, len(texts))
	for _, text := range texts {
		entries = append(entries, fmt.Sprintf(`{"text":%q,"enabled":true,"kind":"map","source":"manual"}`, text))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func callCreateLocalVisibilityRun(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPost, userID, map[string]string{"projectID": projectID, "locationID": locationID}, body)
	rr := httptest.NewRecorder()
	app.handleCreateLocalVisibilityRun(rr, req)
	return rr
}

func callLatestLocalVisibilityRun(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	app.handleGetLatestLocalVisibilityRun(rr, req)
	return rr
}

func callGetLocalVisibilityRun(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, runID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID, "locationID": locationID, "runID": runID}, "")
	rr := httptest.NewRecorder()
	app.handleGetLocalVisibilityRun(rr, req)
	return rr
}

func createLocalVisibilityLocation(t *testing.T, fx localVisibilityFixture, userID, projectID pgtype.UUID, name string) localVisibilityLocationResponse {
	t.Helper()
	// Creation is deliberately unbound and starts with an empty draft; a real
	// flow binds a listing selected from a recorded paid lookup. Grid tests
	// only need a synthetic identity, so bind it directly with SQL and never
	// call the paid lookup provider.
	body := fmt.Sprintf(`{"name":%q,"latitude":27.6942,"longitude":85.3123,"locality":"Testville","localities":["Testville"]}`, name)
	rr := callCreateLocation(t, fx.app, userID, projectID.String(), body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create location status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode location: %v body=%s", err, rr.Body.String())
	}
	if len(created.Queries) != 0 {
		t.Fatalf("new location queries = %#v, want an empty draft", created.Queries)
	}
	rr = callUpdateLocationQueries(t, fx.app, userID, projectID.String(), created.ID, layer4DraftJSON("a", "b", "c", "d", "e"))
	if rr.Code != http.StatusOK {
		t.Fatalf("seed draft status = %d body=%s", rr.Code, rr.Body.String())
	}
	placeID := "place-" + name
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE project_locations SET place_id=$1 WHERE id=$2`, placeID, created.ID); err != nil {
		t.Fatalf("bind synthetic place: %v", err)
	}
	rr = callGetLocation(t, fx.app, userID, projectID.String(), created.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("get location status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response localVisibilityLocationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode location: %v body=%s", err, rr.Body.String())
	}
	if response.PlaceID == nil {
		t.Fatalf("new location place_id is null, want bound before runs")
	}
	return response
}

func findLocalVisibilityCell(t *testing.T, run localVisibilityRunResponse, queryIndex, pointIndex int) localVisibilityCellResponse {
	t.Helper()
	for _, cell := range run.Cells {
		if cell.QueryIndex == queryIndex && cell.PointIndex == pointIndex {
			return cell
		}
	}
	t.Fatalf("cell q%d p%d missing from %d cells", queryIndex, pointIndex, len(run.Cells))
	return localVisibilityCellResponse{}
}

func TestLocalVisibilityValidation(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")
	projectID := "00000000-0000-0000-0000-000000000002"
	locationID := "00000000-0000-0000-0000-000000000003"
	longLocality := strings.Repeat("x", 501)
	validBody := `{"name":"Head Office","latitude":27.6942,"longitude":85.3123}`

	for _, tc := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"create location legacy queries field", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","latitude":27.6,"longitude":85.3,"queries":["a"]}`)
		}},
		{"create location legacy query service field", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","latitude":27.6,"longitude":85.3,"query_service":"coffee"}`)
		}},
		{"create location oversized locality", func() *httptest.ResponseRecorder {
			body := fmt.Sprintf(`{"name":"H","latitude":27.6,"longitude":85.3,"locality":%q}`, longLocality)
			return callCreateLocation(t, app, userID, projectID, body)
		}},
		{"create location oversized localities entry", func() *httptest.ResponseRecorder {
			body := fmt.Sprintf(`{"name":"H","latitude":27.6,"longitude":85.3,"localities":[%q]}`, longLocality)
			return callCreateLocation(t, app, userID, projectID, body)
		}},
		{"create location empty name", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"  ","latitude":27.6,"longitude":85.3}`)
		}},
		{"create location missing latitude", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","longitude":85.3}`)
		}},
		{"create location missing longitude", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","latitude":27.6}`)
		}},
		{"create location missing coordinates", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H"}`)
		}},
		{"create location latitude out of range", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","latitude":91,"longitude":85.3}`)
		}},
		{"create location longitude out of range", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","latitude":27.6,"longitude":-181}`)
		}},
		{"create location unknown field", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":"H","latitude":27.6,"longitude":85.3,"extra":true}`)
		}},
		{"create location invalid json", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, projectID, `{"name":`)
		}},
		{"create location invalid project id", func() *httptest.ResponseRecorder {
			return callCreateLocation(t, app, userID, "not-a-uuid", validBody)
		}},
		{"get location invalid location id", func() *httptest.ResponseRecorder {
			return callGetLocation(t, app, userID, projectID, "not-a-uuid")
		}},
		{"update queries legacy object shape", func() *httptest.ResponseRecorder {
			return callUpdateLocationQueries(t, app, userID, projectID, locationID, `{"queries":["a","b","c","d","e"]}`)
		}},
		{"update queries unknown entry field", func() *httptest.ResponseRecorder {
			return callUpdateLocationQueries(t, app, userID, projectID, locationID, `[{"text":"a","enabled":true,"kind":"map","source":"manual","ordinal":0}]`)
		}},
		{"update queries null body", func() *httptest.ResponseRecorder {
			return callUpdateLocationQueries(t, app, userID, projectID, locationID, `null`)
		}},
		{"update queries invalid json", func() *httptest.ResponseRecorder {
			return callUpdateLocationQueries(t, app, userID, projectID, locationID, `nope`)
		}},
		{"create run radius below minimum", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, app, userID, projectID, locationID, `{"radius_m":500,"expected_credits":135}`)
		}},
		{"create run radius above maximum", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, app, userID, projectID, locationID, `{"radius_m":30000,"expected_credits":135}`)
		}},
		{"create run missing expected credits", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, app, userID, projectID, locationID, `{"radius_m":5000}`)
		}},
		{"create run unknown field", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, app, userID, projectID, locationID, `{"radius_m":5000,"expected_credits":135,"extra":1}`)
		}},
		{"create run invalid location id", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, app, userID, projectID, "not-a-uuid", `{"radius_m":5000,"expected_credits":135}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := tc.call(); rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestLocalVisibilityCreateLocationShape(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "primary")

	if location.ID == "" || location.ProjectID != fx.projectID.String() {
		t.Fatalf("location = %#v", location)
	}
	if location.Name != "primary" || location.PlaceID == nil || *location.PlaceID != "place-primary" {
		t.Fatalf("location identity = %#v", location)
	}
	if location.Latitude != 27.6942 || location.Longitude != 85.3123 {
		t.Fatalf("coordinates = %v,%v", location.Latitude, location.Longitude)
	}
	if len(location.Queries) != 5 || location.Queries[0].Text != "a" {
		t.Fatalf("queries = %#v", location.Queries)
	}
	if location.Locality != "Testville" || len(location.Localities) != 1 || location.Localities[0] != "Testville" {
		t.Fatalf("localities = %#v locality = %q", location.Localities, location.Locality)
	}

	// Any member can read the location; an outsider cannot.
	if rr := callGetLocation(t, fx.app, fx.memberID, fx.projectID.String(), location.ID); rr.Code != http.StatusOK {
		t.Fatalf("member GET location status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := callGetLocation(t, fx.app, fx.outsider, fx.projectID.String(), location.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider GET location status = %d, want 404", rr.Code)
	}
}

func TestLocalVisibilityCrossOrganizationAndWrongProject(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "primary")

	var secondProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'local-visibility-other','https://local-visibility-other.example') RETURNING id`, fx.orgID).Scan(&secondProject); err != nil {
		t.Fatalf("create second project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, secondProject) })

	validQueries := layer4DraftJSON("a", "b", "c", "d", "e")
	for _, tc := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"outsider get", func() *httptest.ResponseRecorder {
			return callGetLocation(t, fx.app, fx.outsider, fx.projectID.String(), location.ID)
		}},
		{"wrong project get", func() *httptest.ResponseRecorder {
			return callGetLocation(t, fx.app, fx.ownerID, secondProject.String(), location.ID)
		}},
		{"outsider update queries", func() *httptest.ResponseRecorder {
			return callUpdateLocationQueries(t, fx.app, fx.outsider, fx.projectID.String(), location.ID, validQueries)
		}},
		{"wrong project update queries", func() *httptest.ResponseRecorder {
			return callUpdateLocationQueries(t, fx.app, fx.ownerID, secondProject.String(), location.ID, validQueries)
		}},
		{"outsider create run", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, fx.app, fx.outsider, fx.projectID.String(), location.ID, `{"radius_m":5000,"expected_credits":135}`)
		}},
		{"wrong project create run", func() *httptest.ResponseRecorder {
			return callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, secondProject.String(), location.ID, `{"radius_m":5000,"expected_credits":135}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := tc.call(); rr.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404; body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestLocalVisibilityRunLifecycle(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "primary")

	if rr := callLatestLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("latest before any run status = %d, want 404", rr.Code)
	}

	// An omitted radius defaults to 5 km.
	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"expected_credits":135}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityRunCreatedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created run: %v body=%s", err, rr.Body.String())
	}
	if created.ExpectedCredits != 135 || created.ReservedCredits != 135 || created.UnconfirmedCalls != 0 {
		t.Fatalf("created run = %#v, want 135/135/0", created)
	}

	rr = callGetLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("get run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var run localVisibilityRunResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v body=%s", err, rr.Body.String())
	}
	if run.Status != "queued" || run.RadiusM != 5000 || run.ExpectedCredits != 135 {
		t.Fatalf("run = %#v", run)
	}
	if len(run.Queries) != 5 || run.Queries[4] != "e" {
		t.Fatalf("run queries = %#v", run.Queries)
	}
	if len(run.Cells) != 45 {
		t.Fatalf("cells = %d, want 45", len(run.Cells))
	}
	if run.ReservedCredits != 135 || run.CreditsUsed != 0 || run.UnconfirmedCalls != 0 {
		t.Fatalf("run credits = reserved %d used %d unconfirmed %d", run.ReservedCredits, run.CreditsUsed, run.UnconfirmedCalls)
	}
	if run.CompletedCells != 0 || run.TotalCells != 45 {
		t.Fatalf("queued run progress = %d/%d, want 0/45", run.CompletedCells, run.TotalCells)
	}
	for _, cell := range run.Cells {
		if cell.CallStatus != "pending" || cell.MatchStatus != "unknown" || cell.Rank != nil || cell.Error != nil {
			t.Fatalf("pending cell = %#v", cell)
		}
	}
	center := findLocalVisibilityCell(t, run, 0, 4)
	if center.Ring != "centre" || center.Sector != "centre" || center.DistanceM != 0 {
		t.Fatalf("center cell = %#v", center)
	}
	corner := findLocalVisibilityCell(t, run, 0, 0)
	if corner.Ring != "corner" || math.Abs(corner.DistanceM-5000) > 1 {
		t.Fatalf("corner cell = %#v", corner)
	}

	// Latest returns the same run.
	rr = callLatestLocalVisibilityRun(t, fx.app, fx.memberID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("latest run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var latest localVisibilityRunResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &latest); err != nil {
		t.Fatalf("decode latest run: %v", err)
	}
	if latest.ID != created.ID {
		t.Fatalf("latest id = %s, want %s", latest.ID, created.ID)
	}
	if latest.CompletedCells != 0 || latest.TotalCells != 45 {
		t.Fatalf("latest queued progress = %d/%d, want 0/45", latest.CompletedCells, latest.TotalCells)
	}

	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known)
		VALUES ($1,0,4,'success_nonempty','found',3,3,TRUE)`, created.ID); err != nil {
		t.Fatalf("insert result: %v", err)
	}
	rr = callGetLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID)
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run with result: %v", err)
	}
	found := findLocalVisibilityCell(t, run, 0, 4)
	if found.CallStatus != "success_nonempty" || found.MatchStatus != "found" || found.Rank == nil || *found.Rank != 3 || found.Credits != 3 {
		t.Fatalf("found cell = %#v", found)
	}
	pending := findLocalVisibilityCell(t, run, 0, 3)
	if pending.CallStatus != "pending" {
		t.Fatalf("adjacent cell = %#v, want pending", pending)
	}
	if run.UnconfirmedCalls != 0 {
		t.Fatalf("unconfirmed calls = %d, want 0 for never-started cells", run.UnconfirmedCalls)
	}
	if run.CompletedCells != 1 || run.TotalCells != 45 {
		t.Fatalf("partial run progress = %d/%d, want 1/45", run.CompletedCells, run.TotalCells)
	}

	// Editing the location queries must not rewrite the frozen run snapshot.
	if rr := callUpdateLocationQueries(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, layer4DraftJSON("v", "w", "x", "y", "z")); rr.Code != http.StatusOK {
		t.Fatalf("update queries status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = callGetLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID)
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run after query edit: %v", err)
	}
	if run.Queries[0] != "a" {
		t.Fatalf("run snapshot queries changed to %#v", run.Queries)
	}

	// A run id from another project is not visible here.
	if rr := callGetLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, "00000000-0000-0000-0000-0000000000ff"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown run status = %d, want 404", rr.Code)
	}

	// A second run while the first is still in flight is a conflict.
	if rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"radius_m":5000,"expected_credits":135}`); rr.Code != http.StatusConflict {
		t.Fatalf("second create run status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestLocalVisibilityBudgetUnavailable(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "primary")

	if _, err := fx.pool.Exec(context.Background(), `UPDATE organization_maps_credit_budgets SET remaining_credits = 0 WHERE organization_id = $1`, fx.orgID); err != nil {
		t.Fatalf("exhaust organization allowance: %v", err)
	}
	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"radius_m":5000,"expected_credits":135}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("unfunded create run status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestNewLocalVisibilityRunResponseTotalCellsFromFrozenSnapshot(t *testing.T) {
	points, err := localvisibility.BuildGeoGrid(27.6942, 85.3123, 5000)
	if err != nil {
		t.Fatalf("build grid: %v", err)
	}
	frozen := points[:4]
	snapshot, err := json.Marshal(localvisibility.LocalRunSnapshot{Queries: []string{"a", "b", "c"}, Points: frozen})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	response, err := newLocalVisibilityRunResponse(sqlc.LocalVisibilityRun{Snapshot: snapshot}, nil, 5)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	if response.TotalCells != 12 {
		t.Fatalf("total_cells = %d, want 12 from 3 frozen queries x 4 frozen points", response.TotalCells)
	}
	if response.CompletedCells != 5 {
		t.Fatalf("completed_cells = %d, want the settled count 5", response.CompletedCells)
	}
}

func TestNewLocalVisibilityRunResponseCompletedCountsSettledFailures(t *testing.T) {
	points, err := localvisibility.BuildGeoGrid(27.6942, 85.3123, 5000)
	if err != nil {
		t.Fatalf("build grid: %v", err)
	}
	snapshot, err := json.Marshal(localvisibility.LocalRunSnapshot{Queries: []string{"a", "b"}, Points: points})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	cells := []sqlc.GetLocalRunCellsRow{
		{QueryIndex: 0, PointIndex: 0, CallStatus: "error", MatchStatus: "unknown", StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}},
		{QueryIndex: 0, PointIndex: 1, CallStatus: "success_nonempty", MatchStatus: "found", Credits: 3, CreditKnown: true},
	}
	response, err := newLocalVisibilityRunResponse(sqlc.LocalVisibilityRun{Snapshot: snapshot}, cells, 2)
	if err != nil {
		t.Fatalf("build response: %v", err)
	}
	if response.CompletedCells != 2 || response.TotalCells != 18 {
		t.Fatalf("progress = %d/%d, want 2/18", response.CompletedCells, response.TotalCells)
	}
}

func localVisibilityQueryOperation(t *testing.T, sql, operation string) string {
	t.Helper()
	marker := "-- name: " + operation
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("local_visibility.sql missing operation %q", operation)
	}
	rest := sql[start+len(marker):]
	if next := strings.Index(rest, "-- name:"); next >= 0 {
		rest = rest[:next]
	}
	return rest
}

func TestLocalVisibilityRunProgressCountQueryContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "db", "queries", "local_visibility.sql"))
	if err != nil {
		t.Fatalf("read local visibility queries: %v", err)
	}
	block := localVisibilityQueryOperation(t, string(raw), "CountLocalVisibilityRunResultsForUser :one")
	for _, needle := range []string{
		"COUNT(*)",
		"FROM local_visibility_results",
		"JOIN organization_members",
		"m.user_id = $4",
	} {
		if !strings.Contains(block, needle) {
			t.Errorf("CountLocalVisibilityRunResultsForUser missing %q", needle)
		}
	}
	if strings.Contains(block, "local_run_cells") {
		t.Error("CountLocalVisibilityRunResultsForUser must count recorded results, not planned cells")
	}
}

func TestLocalVisibilityRunProgressGeneratedSurface(t *testing.T) {
	var q *sqlc.Queries
	_ = q.CountLocalVisibilityRunResultsForUser
}

func TestNewLocalVisibilityRunResponseResultCount(t *testing.T) {
	points, err := localvisibility.BuildGeoGrid(27.6942, 85.3123, 5000)
	if err != nil {
		t.Fatalf("build grid: %v", err)
	}
	snapshot, err := json.Marshal(localvisibility.LocalRunSnapshot{Queries: []string{"a", "b"}, Points: points})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	cells := []sqlc.GetLocalRunCellsRow{
		{QueryIndex: 0, PointIndex: 0, CallStatus: "success_nonempty", MatchStatus: "found", Rank: pgtype.Int4{Int32: 3, Valid: true}, Credits: 3, CreditKnown: true, ResultCount: 3},
		{QueryIndex: 0, PointIndex: 1, CallStatus: "success_empty", MatchStatus: "absent", ResultCount: 0},
		{QueryIndex: 0, PointIndex: 2, CallStatus: "success_nonempty", MatchStatus: "absent", ResultCount: -1},
		{QueryIndex: 0, PointIndex: 3, CallStatus: "request_failed", MatchStatus: "unknown", ResultCount: -1},
	}
	response, err := newLocalVisibilityRunResponse(sqlc.LocalVisibilityRun{Snapshot: snapshot}, cells, len(cells))
	if err != nil {
		t.Fatalf("build response: %v", err)
	}

	found := findLocalVisibilityCell(t, response, 0, 0)
	if found.ResultCount == nil || *found.ResultCount != 3 {
		t.Fatalf("found result_count = %#v, want 3", found.ResultCount)
	}
	if found.Rank == nil || *found.Rank != 3 {
		t.Fatalf("found rank = %#v, want the stored 3", found.Rank)
	}
	empty := findLocalVisibilityCell(t, response, 0, 1)
	if empty.ResultCount == nil || *empty.ResultCount != 0 {
		t.Fatalf("empty result_count = %#v, want 0", empty.ResultCount)
	}
	unreadable := findLocalVisibilityCell(t, response, 0, 2)
	if unreadable.ResultCount != nil {
		t.Fatalf("unreadable result_count = %#v, want null", unreadable.ResultCount)
	}
	failed := findLocalVisibilityCell(t, response, 0, 3)
	if failed.ResultCount != nil {
		t.Fatalf("failed result_count = %#v, want null", failed.ResultCount)
	}
	pending := findLocalVisibilityCell(t, response, 1, 0)
	if pending.ResultCount != nil {
		t.Fatalf("pending result_count = %#v, want null", pending.ResultCount)
	}
}

func TestLocalVisibilityResultCountQueryContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "db", "queries", "local_visibility.sql"))
	if err != nil {
		t.Fatalf("read local visibility queries: %v", err)
	}
	block := localVisibilityQueryOperation(t, string(raw), "GetLocalRunCells :many")
	for _, needle := range []string{
		"result_count",
		"jsonb_array_length(r.raw_response->'places')",
		"jsonb_typeof(r.raw_response->'places') = 'array'",
		"'success_nonempty','success_empty'",
		"ELSE -1",
	} {
		if !strings.Contains(block, needle) {
			t.Errorf("GetLocalRunCells missing %q", needle)
		}
	}
	// The projection is a raw stored array length, never a distinct competitor
	// count or a capped value.
	for _, forbidden := range []string{"DISTINCT", "LEAST("} {
		if strings.Contains(block, forbidden) {
			t.Errorf("GetLocalRunCells result_count must not use %q", forbidden)
		}
	}
}

func TestLocalVisibilityResultCountProjection(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	fx.fundLocalVisibilityBudgets(t)
	location := createLocalVisibilityLocation(t, fx, fx.ownerID, fx.projectID, "primary")

	rr := callCreateLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, `{"radius_m":5000,"expected_credits":135}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created localVisibilityRunCreatedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created run: %v body=%s", err, rr.Body.String())
	}

	insert := `INSERT INTO local_visibility_results
		(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known,raw_response,error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
	// Three stored places including a repeated target id and an id-less entry:
	// the count is the stored array length, never a unique-competitor count.
	threePlaces := []byte(`{"ll":"27.6,85.3","places":[{"position":1,"placeId":"target"},{"position":2,"placeId":"target"},{"position":3}]}`)
	if _, err := fx.pool.Exec(fx.ctx, insert, created.ID, 0, 4, "success_nonempty", "found", pgtype.Int4{Int32: 3, Valid: true}, 3, true, threePlaces, nil); err != nil {
		t.Fatalf("insert found result: %v", err)
	}
	// A succeeded call with an empty stored array projects 0.
	if _, err := fx.pool.Exec(fx.ctx, insert, created.ID, 0, 3, "success_empty", "absent", pgtype.Int4{}, 0, true, []byte(`{"ll":"27.6,85.3","places":[]}`), nil); err != nil {
		t.Fatalf("insert empty result: %v", err)
	}
	// A succeeded call with no stored response projects null.
	if _, err := fx.pool.Exec(fx.ctx, insert, created.ID, 0, 2, "success_nonempty", "absent", pgtype.Int4{}, 3, true, nil, nil); err != nil {
		t.Fatalf("insert missing-raw result: %v", err)
	}
	// A succeeded call whose stored places is not an array projects null.
	if _, err := fx.pool.Exec(fx.ctx, insert, created.ID, 0, 5, "success_nonempty", "absent", pgtype.Int4{}, 3, true, []byte(`{"ll":"27.6,85.3","places":{"not":"array"}}`), nil); err != nil {
		t.Fatalf("insert bad-raw result: %v", err)
	}
	// A failed call projects null even though it carries no ranks.
	if _, err := fx.pool.Exec(fx.ctx, insert, created.ID, 0, 1, "request_failed", "unknown", pgtype.Int4{}, 3, true, nil, "provider error"); err != nil {
		t.Fatalf("insert failed result: %v", err)
	}
	// q0p0 stays pending: no result row exists for it.

	rr = callGetLocalVisibilityRun(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID, created.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("get run status = %d body=%s", rr.Code, rr.Body.String())
	}
	var run localVisibilityRunResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v body=%s", err, rr.Body.String())
	}

	found := findLocalVisibilityCell(t, run, 0, 4)
	if found.ResultCount == nil || *found.ResultCount != 3 {
		t.Fatalf("found result_count = %#v, want 3", found.ResultCount)
	}
	if found.Rank == nil || *found.Rank != 3 {
		t.Fatalf("found rank = %#v, want the stored 3", found.Rank)
	}
	empty := findLocalVisibilityCell(t, run, 0, 3)
	if empty.ResultCount == nil || *empty.ResultCount != 0 {
		t.Fatalf("empty result_count = %#v, want 0", empty.ResultCount)
	}
	missingRaw := findLocalVisibilityCell(t, run, 0, 2)
	if missingRaw.ResultCount != nil {
		t.Fatalf("missing-raw result_count = %#v, want null", missingRaw.ResultCount)
	}
	badRaw := findLocalVisibilityCell(t, run, 0, 5)
	if badRaw.ResultCount != nil {
		t.Fatalf("non-array-raw result_count = %#v, want null", badRaw.ResultCount)
	}
	failed := findLocalVisibilityCell(t, run, 0, 1)
	if failed.ResultCount != nil {
		t.Fatalf("failed result_count = %#v, want null", failed.ResultCount)
	}
	pending := findLocalVisibilityCell(t, run, 0, 0)
	if pending.ResultCount != nil {
		t.Fatalf("pending result_count = %#v, want null", pending.ResultCount)
	}

	// The latest-run endpoint shares the same projection.
	rr = callLatestLocalVisibilityRun(t, fx.app, fx.memberID, fx.projectID.String(), location.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("latest run status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode latest run: %v body=%s", err, rr.Body.String())
	}
	latestFound := findLocalVisibilityCell(t, run, 0, 4)
	if latestFound.ResultCount == nil || *latestFound.ResultCount != 3 {
		t.Fatalf("latest found result_count = %#v, want 3", latestFound.ResultCount)
	}
}
