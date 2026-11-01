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
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

func TestCompetitorKeywordSelectionsRoutesRegistered(t *testing.T) {
	app := &App{Config: config.Config{}}
	routes := map[string]bool{}
	if err := chi.Walk(app.Router().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	for _, want := range []string{
		"GET /projects/{projectID}/competitor-keyword-selections",
		"PUT /projects/{projectID}/competitor-keyword-selections",
		"DELETE /projects/{projectID}/competitor-keyword-selections",
	} {
		if !routes[want] {
			t.Errorf("route %q is not registered", want)
		}
	}
}

func TestHandleCompetitorKeywordSelectionsInvalidIDs(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")

	if rr := callCompetitorSelections(t, app, http.MethodGet, userID, "not-a-uuid", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("GET bad project status = %d, want 400", rr.Code)
	}
	if rr := callCompetitorSelections(t, app, http.MethodPut, userID, "not-a-uuid", `{"competitor_id":"not-a-uuid","keyword":"x","checked":true}`); rr.Code != http.StatusBadRequest {
		t.Errorf("PUT bad project status = %d, want 400", rr.Code)
	}
	if rr := callCompetitorSelections(t, app, http.MethodDelete, userID, "not-a-uuid", `{"competitor_id":"not-a-uuid","keyword":"x"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("DELETE bad project status = %d, want 400", rr.Code)
	}
	if rr := callCompetitorSelections(t, app, http.MethodPut, userID, "00000000-0000-0000-0000-000000000001", `{"competitor_id":"bad","keyword":"x","checked":true}`); rr.Code != http.StatusBadRequest {
		t.Errorf("PUT bad competitor status = %d, want 400", rr.Code)
	}
	if rr := callCompetitorSelections(t, app, http.MethodDelete, userID, "00000000-0000-0000-0000-000000000001", `{"competitor_id":"bad","keyword":"x"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("DELETE bad competitor status = %d, want 400", rr.Code)
	}
}

func TestNewCompetitorKeywordSelectionsResponseGrouping(t *testing.T) {
	var first, second pgtype.UUID
	_ = first.Scan("00000000-0000-0000-0000-000000000011")
	_ = second.Scan("00000000-0000-0000-0000-000000000022")
	rows := []sqlc.ProjectCompetitorKeywordSelection{
		{CompetitorID: first, Keyword: "trail boots", Kind: projectkeywords.ProjectKeywordKindNonBrand},
		{CompetitorID: second, Keyword: "hiking socks", Kind: projectkeywords.ProjectKeywordKindNonBrand},
		{CompetitorID: first, Keyword: "Acme", Kind: projectkeywords.ProjectKeywordKindBrand},
		{CompetitorID: first, Keyword: "camp stoves", Kind: projectkeywords.ProjectKeywordKindNonBrand},
	}
	response := newCompetitorSelectionsResponse(rows)
	if len(response.Selections) != 2 {
		t.Fatalf("selections = %#v, want 2 competitor groups", response.Selections)
	}
	if response.Selections[0].CompetitorID != first.String() {
		t.Fatalf("first group = %#v, want competitor %s", response.Selections[0], first.String())
	}
	if len(response.Selections[0].Keywords) != 2 {
		t.Fatalf("first keywords = %#v, want brand filtered to 2 non-brand", response.Selections[0].Keywords)
	}
	for _, keyword := range response.Selections[0].Keywords {
		if keyword.Kind != projectkeywords.ProjectKeywordKindNonBrand {
			t.Fatalf("keyword = %#v, want non_brand only", keyword)
		}
	}

	empty := newCompetitorSelectionsResponse(nil)
	if empty.Selections == nil || len(empty.Selections) != 0 {
		t.Fatalf("empty selections = %#v, want non-nil empty", empty.Selections)
	}
}

// newCompetitorSelectionsTestPool connects to an explicit disposable test
// database only. It never falls back to DATABASE_URL, so these tests cannot
// mutate a development or production database.
func newCompetitorSelectionsTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("PROJECT_KEYWORDS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PROJECT_KEYWORDS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("competitor selections test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"project_keywords", "project_competitors", "project_competitor_keyword_selections", "competitor_keyword_analysis"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.`+table+`')::text`).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("%s table is not migrated in the test database", table)
		}
	}
	return sqlc.New(pool), pool, ctx
}

type competitorSelectionsFixture struct {
	app          *App
	pool         *pgxpool.Pool
	ctx          context.Context
	orgID        pgtype.UUID
	projectID    pgtype.UUID
	competitorID pgtype.UUID
	ownerID      pgtype.UUID
	memberID     pgtype.UUID
	outsider     pgtype.UUID
}

func newCompetitorSelectionsFixture(t *testing.T) competitorSelectionsFixture {
	t.Helper()
	queries, pool, ctx := newCompetitorSelectionsTestPool(t)
	name := fmt.Sprintf("competitor-selections-test-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
			VALUES ('competitor-selections-test', $1, $2) RETURNING id`, name+role, name+role+"@example.com").Scan(&userID); err != nil {
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
		VALUES ($1,'competitor-selections-test','https://selections.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var competitorID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_competitors (project_id, seed_url, name)
		VALUES ($1,'https://rival.example','Rival') RETURNING id`, projectID).Scan(&competitorID); err != nil {
		t.Fatalf("create competitor: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
	})
	return competitorSelectionsFixture{
		app:          &App{DB: pool, Queries: queries},
		pool:         pool,
		ctx:          ctx,
		orgID:        orgID,
		projectID:    projectID,
		competitorID: competitorID,
		ownerID:      ownerID,
		memberID:     memberID,
		outsider:     outsider,
	}
}

func seedCompetitorAnalysis(t *testing.T, fx competitorSelectionsFixture, keyword, kind string) {
	t.Helper()
	key := projectkeywords.NormalizeProjectKeywordKey(keyword)
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO competitor_keyword_analysis (competitor_id, keyword, normalized_keyword, kind)
		VALUES ($1,$2,$3,$4) ON CONFLICT (competitor_id, normalized_keyword) DO UPDATE SET kind = EXCLUDED.kind`,
		fx.competitorID, keyword, key, kind); err != nil {
		t.Fatalf("seed analysis: %v", err)
	}
}

func decodeCompetitorSelections(t *testing.T, rr *httptest.ResponseRecorder) competitorSelectionsResponse {
	t.Helper()
	var response competitorSelectionsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	return response
}

func selectionCount(t *testing.T, fx competitorSelectionsFixture) int {
	t.Helper()
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM project_competitor_keyword_selections WHERE project_id = $1`, fx.projectID).Scan(&count); err != nil {
		t.Fatalf("count selections: %v", err)
	}
	return count
}

func putSelectionBody(fx competitorSelectionsFixture, keyword string, checked bool) string {
	return fmt.Sprintf(`{"competitor_id":%q,"keyword":%q,"checked":%v}`, fx.competitorID.String(), keyword, checked)
}

func TestCompetitorKeywordSelectionCheckPersistsAcrossReloads(t *testing.T) {
	fx := newCompetitorSelectionsFixture(t)
	seedCompetitorAnalysis(t, fx, "trail boots", projectkeywords.ProjectKeywordKindNonBrand)

	rr := callCompetitorSelections(t, fx.app, http.MethodGet, fx.ownerID, fx.projectID.String(), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("initial GET status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeCompetitorSelections(t, rr); len(got.Selections) != 0 {
		t.Fatalf("initial selections = %#v, want empty", got.Selections)
	}

	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "  Trail   Boots ", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT check status = %d body=%s", rr.Code, rr.Body.String())
	}
	checked := decodeCompetitorSelections(t, rr)
	if len(checked.Selections) != 1 || len(checked.Selections[0].Keywords) != 1 || checked.Selections[0].Keywords[0].Keyword != "Trail Boots" {
		t.Fatalf("checked selections = %#v, want one normalized Trail Boots row", checked.Selections)
	}
	if got := selectionCount(t, fx); got != 1 {
		t.Fatalf("stored rows = %d, want 1", got)
	}

	// Reload: a fresh GET must still show the checked keyword.
	rr = callCompetitorSelections(t, fx.app, http.MethodGet, fx.ownerID, fx.projectID.String(), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("reload GET status = %d", rr.Code)
	}
	if got := decodeCompetitorSelections(t, rr); len(got.Selections) != 1 || len(got.Selections[0].Keywords) != 1 {
		t.Fatalf("reload selections = %#v, want the checked keyword", got.Selections)
	}

	// Re-checking the same phrase stays idempotent at one row.
	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "trail boots", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("idempotent PUT status = %d", rr.Code)
	}
	if got := selectionCount(t, fx); got != 1 {
		t.Fatalf("stored rows after idempotent check = %d, want 1", got)
	}
}

func TestCompetitorKeywordSelectionUncheckRemoves(t *testing.T) {
	fx := newCompetitorSelectionsFixture(t)

	rr := callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "camp stoves", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT check status = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "camp stoves", false))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT uncheck status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeCompetitorSelections(t, rr); len(got.Selections) != 0 {
		t.Fatalf("selections after uncheck = %#v, want empty", got.Selections)
	}
	if got := selectionCount(t, fx); got != 0 {
		t.Fatalf("stored rows after uncheck = %d, want 0", got)
	}

	// Unchecking a missing row is a no-op success.
	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "camp stoves", false))
	if rr.Code != http.StatusOK {
		t.Fatalf("repeat uncheck status = %d", rr.Code)
	}

	// DELETE removes a checked keyword too.
	if rr := callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "camp stoves", true)); rr.Code != http.StatusOK {
		t.Fatalf("PUT re-check status = %d", rr.Code)
	}
	rr = callCompetitorSelections(t, fx.app, http.MethodDelete, fx.ownerID, fx.projectID.String(),
		fmt.Sprintf(`{"competitor_id":%q,"keyword":"camp stoves"}`, fx.competitorID.String()))
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := selectionCount(t, fx); got != 0 {
		t.Fatalf("stored rows after DELETE = %d, want 0", got)
	}
}

func TestCompetitorKeywordSelectionRejectsBrand(t *testing.T) {
	fx := newCompetitorSelectionsFixture(t)
	seedCompetitorAnalysis(t, fx, "Acme", projectkeywords.ProjectKeywordKindBrand)
	seedCompetitorAnalysis(t, fx, "trail boots", projectkeywords.ProjectKeywordKindNonBrand)

	rr := callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "ACME", true))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("brand PUT status = %d, want 400", rr.Code)
	}
	if got := selectionCount(t, fx); got != 0 {
		t.Fatalf("stored rows after brand PUT = %d, want 0", got)
	}

	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "trail boots", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("non-brand PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCompetitorKeywordSelectionPermissionsAndIsolation(t *testing.T) {
	fx := newCompetitorSelectionsFixture(t)

	rr := callCompetitorSelections(t, fx.app, http.MethodGet, fx.memberID, fx.projectID.String(), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("member GET status = %d, want 200", rr.Code)
	}
	rr = callCompetitorSelections(t, fx.app, http.MethodGet, fx.outsider, fx.projectID.String(), "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("outsider GET status = %d, want 404", rr.Code)
	}

	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.memberID, fx.projectID.String(), putSelectionBody(fx, "trail boots", true))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member PUT status = %d, want 403", rr.Code)
	}
	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.outsider, fx.projectID.String(), putSelectionBody(fx, "trail boots", true))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("outsider PUT status = %d, want 404", rr.Code)
	}

	var otherCompetitor pgtype.UUID
	var otherProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'selections-other','https://other.example') RETURNING id`, fx.orgID).Scan(&otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, otherProject) })
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_competitors (project_id, seed_url, name)
		VALUES ($1,'https://other-rival.example','Other') RETURNING id`, otherProject).Scan(&otherCompetitor); err != nil {
		t.Fatalf("create other competitor: %v", err)
	}
	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(),
		fmt.Sprintf(`{"competitor_id":%q,"keyword":"trail boots","checked":true}`, otherCompetitor.String()))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("foreign competitor PUT status = %d, want 404", rr.Code)
	}
}

func TestCompetitorKeywordSelectionCombinedIncludesChecked(t *testing.T) {
	fx := newCompetitorSelectionsFixture(t)

	rr := callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "trail boots", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT check status = %d body=%s", rr.Code, rr.Body.String())
	}

	lists, err := projectkeywords.LoadProjectKeywordLists(fx.ctx, fx.app.Queries, fx.projectID)
	if err != nil {
		t.Fatalf("load lists: %v", err)
	}
	if len(lists.UserDefined) != 0 || len(lists.RevserpSuggested) != 0 {
		t.Fatalf("per-source = %d/%d, want 0/0", len(lists.UserDefined), len(lists.RevserpSuggested))
	}
	if len(lists.Combined) != 1 {
		t.Fatalf("combined = %#v, want 1 entry", lists.Combined)
	}
	entry := lists.Combined[0]
	if entry.Keyword != "trail boots" || entry.Kind != projectkeywords.ProjectKeywordKindNonBrand {
		t.Fatalf("combined entry = %#v, want trail boots/non_brand", entry)
	}
	if len(entry.Sources) != 1 || entry.Sources[0] != projectkeywords.ProjectKeywordSourceCompetitor {
		t.Fatalf("combined sources = %v, want [competitor]", entry.Sources)
	}

	rr = callCompetitorSelections(t, fx.app, http.MethodPut, fx.ownerID, fx.projectID.String(), putSelectionBody(fx, "trail boots", false))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT uncheck status = %d", rr.Code)
	}
	lists, err = projectkeywords.LoadProjectKeywordLists(fx.ctx, fx.app.Queries, fx.projectID)
	if err != nil {
		t.Fatalf("load lists after uncheck: %v", err)
	}
	if len(lists.Combined) != 0 {
		t.Fatalf("combined after uncheck = %#v, want empty", lists.Combined)
	}
}

func callCompetitorSelections(t *testing.T, app *App, method string, userID pgtype.UUID, projectID, body string) *httptest.ResponseRecorder {
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
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		app.handleListCompetitorKeywordSelections(rr, req)
	case http.MethodPut:
		app.handlePutCompetitorKeywordSelection(rr, req)
	case http.MethodDelete:
		app.handleDeleteCompetitorKeywordSelection(rr, req)
	default:
		t.Fatalf("unknown method %s", method)
	}
	return rr
}
