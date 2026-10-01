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

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestKeywordListsRoutesRegistered(t *testing.T) {
	app := &App{Config: config.Config{}}
	routes := map[string]bool{}
	if err := chi.Walk(app.Router().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	for _, want := range []string{
		"GET /projects/{projectID}/keyword-lists",
		"POST /projects/{projectID}/keyword-lists",
		"DELETE /projects/{projectID}/keyword-lists/{keywordID}",
		"GET /projects/{projectID}/keywords",
	} {
		if !routes[want] {
			t.Errorf("route %q is not registered", want)
		}
	}
}

func TestHandleKeywordListsInvalidIDs(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")

	if rr := callKeywordLists(t, app, http.MethodGet, userID, "not-a-uuid", "", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("GET bad project status = %d, want 400", rr.Code)
	}
	if rr := callKeywordLists(t, app, http.MethodPost, userID, "not-a-uuid", "", `{"keyword":"x","kind":"brand"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("POST bad project status = %d, want 400", rr.Code)
	}
	if rr := callKeywordLists(t, app, http.MethodDelete, userID, "00000000-0000-0000-0000-000000000001", "bad-id", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("DELETE bad keyword status = %d, want 400", rr.Code)
	}
}

// newKeywordListsTestPool connects to an explicit disposable test database
// only. It never falls back to DATABASE_URL, so these tests cannot mutate a
// development or production database.
func newKeywordListsTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("PROJECT_KEYWORDS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PROJECT_KEYWORDS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("project keywords test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var regclass string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.project_keywords')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("project_keywords table is not migrated in the test database")
	}
	return sqlc.New(pool), pool, ctx
}

type keywordListsFixture struct {
	app       *App
	pool      *pgxpool.Pool
	ctx       context.Context
	orgID     pgtype.UUID
	projectID pgtype.UUID
	ownerID   pgtype.UUID
	memberID  pgtype.UUID
	outsider  pgtype.UUID
}

func newKeywordListsFixture(t *testing.T) keywordListsFixture {
	t.Helper()
	queries, pool, ctx := newKeywordListsTestPool(t)
	name := fmt.Sprintf("keyword-lists-test-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
			VALUES ('kw-lists-test', $1, $2) RETURNING id`, name+role, name+role+"@example.com").Scan(&userID); err != nil {
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
		VALUES ($1,'kw-lists-test','https://kw-lists.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, projectID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
	})
	return keywordListsFixture{
		app:       &App{DB: pool, Queries: queries},
		pool:      pool,
		ctx:       ctx,
		orgID:     orgID,
		projectID: projectID,
		ownerID:   ownerID,
		memberID:  memberID,
		outsider:  outsider,
	}
}

func decodeKeywordLists(t *testing.T, rr *httptest.ResponseRecorder) projectKeywordListsResponse {
	t.Helper()
	var response projectKeywordListsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	return response
}

func workerJobCount(t *testing.T, fx keywordListsFixture) int {
	t.Helper()
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM ai_worker_jobs WHERE project_id = $1`, fx.projectID).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return count
}

func TestGetKeywordListsPermissions(t *testing.T) {
	fx := newKeywordListsFixture(t)

	rr := callKeywordLists(t, fx.app, http.MethodGet, fx.ownerID, fx.projectID.String(), "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("owner GET status = %d body=%s", rr.Code, rr.Body.String())
	}
	owner := decodeKeywordLists(t, rr)
	if !owner.CanManageKeywords {
		t.Error("owner can_manage_keywords = false, want true")
	}
	for name, raw := range map[string]string{
		"user_defined":      `"user_defined":[]`,
		"revserp_suggested": `"revserp_suggested":[]`,
		"combined":          `"combined":[]`,
	} {
		if !strings.Contains(rr.Body.String(), raw) {
			t.Errorf("empty GET body missing non-null %s: %s", name, rr.Body.String())
		}
	}

	rr = callKeywordLists(t, fx.app, http.MethodGet, fx.memberID, fx.projectID.String(), "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("member GET status = %d", rr.Code)
	}
	if decodeKeywordLists(t, rr).CanManageKeywords {
		t.Error("member can_manage_keywords = true, want false")
	}

	rr = callKeywordLists(t, fx.app, http.MethodGet, fx.outsider, fx.projectID.String(), "", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("outsider GET status = %d, want 404", rr.Code)
	}
}

func TestAddKeywordPermissionsAndShape(t *testing.T) {
	fx := newKeywordListsFixture(t)
	beforeJobs := workerJobCount(t, fx)

	rr := callKeywordLists(t, fx.app, http.MethodPost, fx.memberID, fx.projectID.String(), "", `{"keyword":"member try","kind":"brand"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member POST status = %d, want 403", rr.Code)
	}

	rr = callKeywordLists(t, fx.app, http.MethodPost, fx.outsider, fx.projectID.String(), "", `{"keyword":"outsider try","kind":"brand"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("outsider POST status = %d, want 404", rr.Code)
	}

	rr = callKeywordLists(t, fx.app, http.MethodPost, fx.ownerID, fx.projectID.String(), "", `{"keyword":"  Acme Boots ","kind":"brand"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner POST status = %d body=%s", rr.Code, rr.Body.String())
	}
	added := decodeKeywordLists(t, rr)
	if len(added.UserDefined) != 1 || added.UserDefined[0].Keyword != "Acme Boots" {
		t.Fatalf("user_defined = %#v", added.UserDefined)
	}
	if len(added.Combined) != 1 || len(added.Combined[0].Sources) != 1 || added.Combined[0].Sources[0] != "user" {
		t.Fatalf("combined = %#v", added.Combined)
	}

	// Idempotent re-add returns the same row with 200.
	again := callKeywordLists(t, fx.app, http.MethodPost, fx.ownerID, fx.projectID.String(), "", `{"keyword":"acme boots","kind":"brand"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("idempotent POST status = %d", again.Code)
	}
	if decodeKeywordLists(t, again).UserDefined[0].ID != added.UserDefined[0].ID {
		t.Error("idempotent POST returned a different row")
	}

	rr = callKeywordLists(t, fx.app, http.MethodPost, fx.ownerID, fx.projectID.String(), "", `{"keyword":"ACME BOOTS","kind":"non_brand"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("opposite kind POST status = %d, want 409", rr.Code)
	}

	rr = callKeywordLists(t, fx.app, http.MethodPost, fx.ownerID, fx.projectID.String(), "", `{"keyword":"x","kind":"target"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad kind POST status = %d, want 400", rr.Code)
	}

	rr = callKeywordLists(t, fx.app, http.MethodPost, fx.ownerID, fx.projectID.String(), "", `{"keyword":"   ","kind":"brand"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("blank POST status = %d, want 400", rr.Code)
	}

	if got := workerJobCount(t, fx); got != beforeJobs {
		t.Errorf("worker jobs = %d, want %d: keyword edits must not enqueue prompt jobs", got, beforeJobs)
	}
}

func TestDeleteKeywordIsolation(t *testing.T) {
	fx := newKeywordListsFixture(t)
	var secondID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'kw-lists-other','https://kw-lists-other.example') RETURNING id`, fx.orgID).Scan(&secondID); err != nil {
		t.Fatalf("create second project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, secondID) })

	rr := callKeywordLists(t, fx.app, http.MethodPost, fx.ownerID, fx.projectID.String(), "", `{"keyword":"doomed","kind":"brand"}`)
	mine := decodeKeywordLists(t, rr).UserDefined[0].ID

	var revserpID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1,'suggested','suggested','non_brand','revserp') RETURNING id`, fx.projectID).Scan(&revserpID); err != nil {
		t.Fatalf("seed revserp: %v", err)
	}
	var foreignID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1,'foreign','foreign','brand','user') RETURNING id`, secondID).Scan(&foreignID); err != nil {
		t.Fatalf("seed foreign: %v", err)
	}

	beforeJobs := workerJobCount(t, fx)

	rr = callKeywordLists(t, fx.app, http.MethodDelete, fx.memberID, fx.projectID.String(), mine, "")
	if rr.Code != http.StatusForbidden {
		t.Errorf("member DELETE status = %d, want 403", rr.Code)
	}

	for name, id := range map[string]string{"revserp": revserpID.String(), "foreign": foreignID.String(), "missing": "00000000-0000-0000-0000-000000000099"} {
		rr = callKeywordLists(t, fx.app, http.MethodDelete, fx.ownerID, fx.projectID.String(), id, "")
		if rr.Code != http.StatusNotFound {
			t.Errorf("DELETE %s status = %d, want 404", name, rr.Code)
		}
	}

	rr = callKeywordLists(t, fx.app, http.MethodDelete, fx.ownerID, fx.projectID.String(), mine, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("owner DELETE status = %d body=%s", rr.Code, rr.Body.String())
	}
	after := decodeKeywordLists(t, rr)
	if len(after.UserDefined) != 0 {
		t.Fatalf("user_defined after delete = %#v", after.UserDefined)
	}
	if len(after.RevserpSuggested) != 1 {
		t.Fatalf("revserp after delete = %#v, want suggested row kept", after.RevserpSuggested)
	}

	if got := workerJobCount(t, fx); got != beforeJobs {
		t.Errorf("worker jobs = %d, want %d: keyword edits must not enqueue prompt jobs", got, beforeJobs)
	}
}

func callKeywordLists(t *testing.T, app *App, method string, userID pgtype.UUID, projectID, keywordID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/", reader)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("projectID", projectID)
	if keywordID != "" {
		routeCtx.URLParams.Add("keywordID", keywordID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		app.handleGetProjectKeywordLists(rr, req)
	case http.MethodPost:
		app.handleAddProjectKeyword(rr, req)
	case http.MethodDelete:
		app.handleDeleteProjectKeyword(rr, req)
	default:
		t.Fatalf("unknown method %s", method)
	}
	return rr
}
