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

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func locationAuditTestQueries(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	raw, envName := locationAuditTestDatabaseURL()
	if raw == "" {
		t.Skip("LOCATION_AI_AUDIT_TEST_DATABASE_URL and LOCAL_SEO_TEST_DATABASE_URL are not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", envName, err)
	}
	if name := strings.Trim(parsed.Path, "/"); !strings.HasPrefix(name, "revserp_layer_c_") {
		t.Fatalf("refusing %s database %q: want a revserp_layer_c_* scratch database", envName, name)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("refusing %s host %q: want 127.0.0.1 or localhost", envName, host)
	}
	if parsed.Port() != "5432" {
		t.Fatalf("refusing %s port %q: want 5432", envName, parsed.Port())
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, raw, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("location audit test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'ai_audits' AND column_name = 'location_id')`).Scan(&migrated); err != nil || !migrated {
		t.Skip("ai_audits.location_id is not migrated in the test database")
	}
	var profilesReady bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.location_business_profiles') IS NOT NULL`).Scan(&profilesReady); err != nil || !profilesReady {
		t.Skip("location_business_profiles is not migrated in the test database")
	}
	return sqlc.New(pool), pool, ctx
}

func locationAuditTestDatabaseURL() (string, string) {
	if databaseURL := strings.TrimSpace(os.Getenv("LOCATION_AI_AUDIT_TEST_DATABASE_URL")); databaseURL != "" {
		return databaseURL, "LOCATION_AI_AUDIT_TEST_DATABASE_URL"
	}
	if databaseURL := strings.TrimSpace(os.Getenv("LOCAL_SEO_TEST_DATABASE_URL")); databaseURL != "" {
		return databaseURL, "LOCAL_SEO_TEST_DATABASE_URL"
	}
	return "", ""
}

type locationAuditFixture struct {
	app                 *App
	queries             *sqlc.Queries
	pool                *pgxpool.Pool
	ctx                 context.Context
	orgID               pgtype.UUID
	ownerID             pgtype.UUID
	outsiderID          pgtype.UUID
	projectID           pgtype.UUID
	otherProjectID      pgtype.UUID
	locationID          pgtype.UUID
	emptyLocationID     pgtype.UUID
	noProfileLocationID pgtype.UUID
	crawlID             pgtype.UUID
	otherCrawlID        pgtype.UUID
}

func newLocationAuditFixture(t *testing.T) locationAuditFixture {
	t.Helper()
	queries, pool, ctx := locationAuditTestQueries(t)
	name := fmt.Sprintf("location-audit-test-%d", time.Now().UnixNano())

	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`,
			name+role, name+role+"@example.com").Scan(&userID); err != nil {
			t.Fatalf("create user %s: %v", role, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		})
		if role != "outsider" {
			if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
				t.Fatalf("add member: %v", err)
			}
		}
		return userID
	}
	ownerID := newUser("owner")
	outsideID := newUser("outsider")

	newProject := func(projectName string) pgtype.UUID {
		var projectID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,$2,'https://example.com') RETURNING id`,
			orgID, projectName).Scan(&projectID); err != nil {
			t.Fatalf("create project: %v", err)
		}
		return projectID
	}
	projectID := newProject(name)
	otherProjectID := newProject(name + "-other")

	newCrawl := func(project pgtype.UUID) pgtype.UUID {
		var crawlID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, project).Scan(&crawlID); err != nil {
			t.Fatalf("create crawl: %v", err)
		}
		return crawlID
	}

	newLocation := func(project pgtype.UUID, locationName string, seedPrompts []string) pgtype.UUID {
		var locationID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,$2,27.7172,85.3240) RETURNING id`,
			project, locationName).Scan(&locationID); err != nil {
			t.Fatalf("create location: %v", err)
		}
		prompts, err := json.Marshal(seedPrompts)
		if err != nil {
			t.Fatalf("marshal seed prompts: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE location_business_profiles SET seed_prompts = $1 WHERE location_id = $2`, prompts, locationID); err != nil {
			t.Fatalf("set location seed prompts: %v", err)
		}
		return locationID
	}

	if err := func() error {
		_, err := queries.UpsertProjectAIQuestions(ctx, sqlc.UpsertProjectAIQuestionsParams{
			ProjectID:         projectID,
			Questions:         []byte(`["What firms offer seo audits?"]`),
			LocationQuestions: []byte(`[]`),
			GenerationModel:   "test",
		})
		return err
	}(); err != nil {
		t.Fatalf("seed project questions: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO project_business_profile (project_id, brand_name, website_url) VALUES ($1,'Location Parent','https://example.com')`, projectID); err != nil {
		t.Fatalf("seed parent business profile: %v", err)
	}

	noProfileLocationID := newLocation(projectID, name+"-no-profile", []string{"what seo firms exist?"})
	if _, err := pool.Exec(ctx, `DELETE FROM location_business_profiles WHERE location_id = $1`, noProfileLocationID); err != nil {
		t.Fatalf("drop location profile: %v", err)
	}

	return locationAuditFixture{
		app:                 &App{DB: pool, Queries: queries},
		queries:             queries,
		pool:                pool,
		ctx:                 ctx,
		orgID:               orgID,
		ownerID:             ownerID,
		outsiderID:          outsideID,
		projectID:           projectID,
		otherProjectID:      otherProjectID,
		locationID:          newLocation(projectID, name+"-office", []string{"what seo firms exist?"}),
		emptyLocationID:     newLocation(projectID, name+"-empty", []string{}),
		noProfileLocationID: noProfileLocationID,
		crawlID:             newCrawl(projectID),
		otherCrawlID:        newCrawl(otherProjectID),
	}
}

func newCreateAIAuditHTTPRequest(userID, projectID pgtype.UUID, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/projects/"+projectID.String()+"/ai-audits", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", projectID.String())
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return request.WithContext(ctx)
}

func decodeCreatedAIAudit(t *testing.T, recorder *httptest.ResponseRecorder) aiAuditResponse {
	t.Helper()
	var response aiAuditResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return response
}

func locationAuditMonthlyUsage(t *testing.T, fx locationAuditFixture) int64 {
	t.Helper()
	var used int64
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM ai_workspace_monthly_usage WHERE organization_id = $1`, fx.orgID).Scan(&used); err != nil {
		t.Fatalf("count usage: %v", err)
	}
	return used
}

func TestCreateAIAuditInvalidInputWithoutDB(t *testing.T) {
	ownerID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	app := &App{}

	for _, test := range []struct {
		name       string
		projectID  string
		body       string
		wantStatus int
		wantError  string
	}{
		{"invalid project id", "not-a-uuid", `{}`, http.StatusBadRequest, "invalid project id"},
		{"invalid location id", projectID.String(), `{"location_id":"not-a-uuid"}`, http.StatusBadRequest, "invalid location id"},
		{"invalid crawl id", projectID.String(), `{"crawl_id":"not-a-uuid"}`, http.StatusBadRequest, "invalid crawl id"},
		{"invalid crawl id with location", projectID.String(), `{"location_id":"` + projectID.String() + `","crawl_id":"not-a-uuid"}`, http.StatusBadRequest, "invalid crawl id"},
		{"missing crawl on project audit", projectID.String(), `{}`, http.StatusBadRequest, "crawl_id is required"},
		{"empty crawl on project audit", projectID.String(), `{"crawl_id":"  "}`, http.StatusBadRequest, "crawl_id is required"},
		{"malformed body", projectID.String(), `not json`, http.StatusBadRequest, "invalid json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/projects/"+test.projectID+"/ai-audits", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("projectID", test.projectID)
			ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
			ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: ownerID}})
			recorder := httptest.NewRecorder()
			app.handleCreateAIAudit(recorder, request.WithContext(ctx))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), test.wantError) {
				t.Fatalf("body = %s, want error containing %q", recorder.Body.String(), test.wantError)
			}
		})
	}
}

func TestParseAIAuditOptionalUUID(t *testing.T) {
	valid := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	id, present, err := parseAIAuditOptionalUUID("")
	if present || err != nil || id.Valid {
		t.Fatalf("empty = %v,%v,%v, want absent", id, present, err)
	}
	id, present, err = parseAIAuditOptionalUUID("   ")
	if present || err != nil || id.Valid {
		t.Fatalf("blank = %v,%v,%v, want absent", id, present, err)
	}
	id, present, err = parseAIAuditOptionalUUID(valid.String())
	if !present || err != nil || id != valid {
		t.Fatalf("valid = %v,%v,%v, want %v present", id, present, err, valid)
	}
	if _, present, err = parseAIAuditOptionalUUID("not-a-uuid"); present || err == nil {
		t.Fatalf("invalid = present %v err %v, want absent with error", present, err)
	}
}

func TestBuildAIAuditResponseLocationID(t *testing.T) {
	zero := pgtype.Timestamptz{}
	id := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	project := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	location := pgtype.UUID{Bytes: [16]byte{5}, Valid: true}

	response := buildAIAuditResponse(id, project, pgtype.UUID{}, location, "queued", pgtype.Int4{}, pgtype.Text{}, zero, zero, zero, zero)
	if response.LocationID != location.String() {
		t.Fatalf("location_id = %q, want %q", response.LocationID, location.String())
	}
	if response.CrawlID != "" {
		t.Fatalf("crawl_id = %q, want absent without a crawl", response.CrawlID)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"location_id":"`+location.String()+`"`) {
		t.Fatalf("json = %s, want location_id present", encoded)
	}

	response = buildAIAuditResponse(id, project, pgtype.UUID{}, pgtype.UUID{}, "queued", pgtype.Int4{}, pgtype.Text{}, zero, zero, zero, zero)
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "location_id") {
		t.Fatalf("json = %s, want location_id omitted for project audits", encoded)
	}
}

func TestAIAuditRunMentionedBranchMapping(t *testing.T) {
	runs := []sqlc.AiAuditRun{
		{MentionedTarget: pgtype.Bool{Bool: true, Valid: true}, MentionedBranch: pgtype.Bool{Bool: true, Valid: true}},
		{MentionedTarget: pgtype.Bool{Bool: true, Valid: true}, MentionedBranch: pgtype.Bool{Bool: false, Valid: true}},
		{MentionedBranch: pgtype.Bool{}},
	}
	responses := newAIAuditRunResponses(runs)
	if responses[0].MentionedBranch == nil || !*responses[0].MentionedBranch {
		t.Fatalf("first mentioned_branch = %+v, want true", responses[0].MentionedBranch)
	}
	if responses[1].MentionedBranch == nil || *responses[1].MentionedBranch {
		t.Fatalf("second mentioned_branch = %+v, want explicit false", responses[1].MentionedBranch)
	}
	if responses[2].MentionedBranch != nil {
		t.Fatalf("third mentioned_branch = %+v, want nil for unknown", responses[2].MentionedBranch)
	}
	encoded, err := json.Marshal(responses[2])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "mentioned_branch") {
		t.Fatalf("json = %s, want mentioned_branch omitted when unknown", encoded)
	}
	encoded, err = json.Marshal(responses[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"mentioned_branch":false`) {
		t.Fatalf("json = %s, want explicit false preserved", encoded)
	}
}

func TestCreateLocationAIAuditValidation(t *testing.T) {
	fx := newLocationAuditFixture(t)

	for _, test := range []struct {
		name       string
		user       func() pgtype.UUID
		project    func() pgtype.UUID
		body       func() string
		wantStatus int
		wantError  string
	}{
		{"unknown location", func() pgtype.UUID { return fx.ownerID }, func() pgtype.UUID { return fx.projectID },
			func() string { return `{"location_id":"00000000-0000-0000-0000-000000000000"}` }, http.StatusNotFound, "location not found"},
		{"no AI questions", func() pgtype.UUID { return fx.ownerID }, func() pgtype.UUID { return fx.projectID },
			func() string { return `{"location_id":"` + fx.emptyLocationID.String() + `"}` }, http.StatusBadRequest, "location AI questions must be set"},
		{"no business profile", func() pgtype.UUID { return fx.ownerID }, func() pgtype.UUID { return fx.projectID },
			func() string { return `{"location_id":"` + fx.noProfileLocationID.String() + `"}` }, http.StatusBadRequest, "location business profile must be configured"},
		{"foreign crawl", func() pgtype.UUID { return fx.ownerID }, func() pgtype.UUID { return fx.projectID },
			func() string {
				return `{"location_id":"` + fx.locationID.String() + `","crawl_id":"` + fx.otherCrawlID.String() + `"}`
			}, http.StatusBadRequest, "crawl does not belong to project"},
		{"unknown crawl", func() pgtype.UUID { return fx.ownerID }, func() pgtype.UUID { return fx.projectID },
			func() string {
				return `{"location_id":"` + fx.locationID.String() + `","crawl_id":"00000000-0000-0000-0000-000000000000"}`
			}, http.StatusBadRequest, "crawl not found"},
		{"outsider", func() pgtype.UUID { return fx.outsiderID }, func() pgtype.UUID { return fx.projectID },
			func() string { return `{"location_id":"` + fx.locationID.String() + `"}` }, http.StatusNotFound, "project not found"},
		{"project audit without questions", func() pgtype.UUID { return fx.ownerID }, func() pgtype.UUID { return fx.otherProjectID },
			func() string { return `{"crawl_id":"` + fx.otherCrawlID.String() + `"}` }, http.StatusBadRequest, "ai questions must be generated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			fx.app.handleCreateAIAudit(recorder, newCreateAIAuditHTTPRequest(test.user(), test.project(), test.body()))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), test.wantError) {
				t.Fatalf("body = %s, want error containing %q", recorder.Body.String(), test.wantError)
			}
		})
	}

	if used := locationAuditMonthlyUsage(t, fx); used != 0 {
		t.Fatalf("monthly usage rows = %d, want 0: invalid creates must never reserve quota", used)
	}
}

func TestCreateLocationAIAuditCrossProjectLocation(t *testing.T) {
	fx := newLocationAuditFixture(t)
	var foreign pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,'foreign',27.7,85.3) RETURNING id`,
		fx.otherProjectID).Scan(&foreign); err != nil {
		t.Fatalf("create foreign location: %v", err)
	}
	recorder := httptest.NewRecorder()
	fx.app.handleCreateAIAudit(recorder, newCreateAIAuditHTTPRequest(fx.ownerID, fx.projectID, `{"location_id":"`+foreign.String()+`"}`))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateLocationAIAuditSuccessAndDuplicateGuard(t *testing.T) {
	fx := newLocationAuditFixture(t)

	recorder := httptest.NewRecorder()
	fx.app.handleCreateAIAudit(recorder, newCreateAIAuditHTTPRequest(fx.ownerID, fx.projectID, `{"location_id":"`+fx.locationID.String()+`"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
	created := decodeCreatedAIAudit(t, recorder)
	if created.LocationID != fx.locationID.String() {
		t.Fatalf("location_id = %q, want %q", created.LocationID, fx.locationID.String())
	}
	if created.CrawlID != "" {
		t.Fatalf("crawl_id = %q, want absent without a crawl", created.CrawlID)
	}
	if !strings.Contains(recorder.Body.String(), `"location_id":"`+fx.locationID.String()+`"`) {
		t.Fatalf("body = %s, want location_id present", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	fx.app.handleCreateAIAudit(recorder, newCreateAIAuditHTTPRequest(fx.ownerID, fx.projectID,
		`{"location_id":"`+fx.locationID.String()+`","crawl_id":"`+fx.crawlID.String()+`"}`))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
	}

	if used := locationAuditMonthlyUsage(t, fx); used != 1 {
		t.Fatalf("monthly usage rows = %d, want 1: the conflict must not reserve again", used)
	}
}

func TestCreateLocationAIAuditWithCrawl(t *testing.T) {
	fx := newLocationAuditFixture(t)
	recorder := httptest.NewRecorder()
	fx.app.handleCreateAIAudit(recorder, newCreateAIAuditHTTPRequest(fx.ownerID, fx.projectID,
		`{"location_id":"`+fx.locationID.String()+`","crawl_id":"`+fx.crawlID.String()+`"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
	created := decodeCreatedAIAudit(t, recorder)
	if created.LocationID != fx.locationID.String() || created.CrawlID != fx.crawlID.String() {
		t.Fatalf("created = %+v, want both location and crawl ids", created)
	}
}

func TestListAIAuditsLocationScope(t *testing.T) {
	fx := newLocationAuditFixture(t)
	insertAudit := func(location *pgtype.UUID, crawl *pgtype.UUID, status string) {
		t.Helper()
		var loc, crawlParam any
		if location != nil {
			loc = *location
		}
		if crawl != nil {
			crawlParam = *crawl
		}
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_audits (project_id, location_id, crawl_id, status) VALUES ($1,$2,$3,$4)`,
			fx.projectID, loc, crawlParam, status); err != nil {
			t.Fatalf("insert audit: %v", err)
		}
	}
	insertAudit(nil, &fx.crawlID, "completed")
	insertAudit(&fx.locationID, nil, "completed")
	insertAudit(&fx.locationID, &fx.crawlID, "failed")

	recorder := httptest.NewRecorder()
	fx.app.handleListAIAudits(recorder, listAIAuditsRequest(fx.ownerID, fx.projectID, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("default status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.AIAudits[0].LocationID != "" || response.Pagination.Total != 1 {
		t.Fatalf("default = %+v, want only the project audit", response.AIAudits)
	}
	if strings.Contains(recorder.Body.String(), `"location_id"`) {
		t.Fatalf("default body = %s, want no location_id key", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	fx.app.handleListAIAudits(recorder, listAIAuditsRequest(fx.ownerID, fx.projectID, "location_id="+fx.locationID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("location status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response = decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 2 || response.Pagination.Total != 2 {
		t.Fatalf("location = %+v, want the 2 location audits", response.AIAudits)
	}
	for _, audit := range response.AIAudits {
		if audit.LocationID != fx.locationID.String() {
			t.Fatalf("audit = %+v, want location_id %q", audit, fx.locationID.String())
		}
	}

	recorder = httptest.NewRecorder()
	fx.app.handleListAIAudits(recorder, listAIAuditsRequest(fx.ownerID, fx.projectID, "location_id="+fx.locationID.String()+"&status=failed"))
	response = decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.AIAudits[0].Status != "failed" {
		t.Fatalf("filtered = %+v, want the single failed location audit", response.AIAudits)
	}
	recorder = httptest.NewRecorder()
	fx.app.handleListAIAudits(recorder, listAIAuditsRequest(fx.ownerID, fx.projectID, "location_id="+fx.locationID.String()+"&limit=1&offset=0"))
	response = decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.Pagination.Total != 2 || response.Pagination.Count != 1 {
		t.Fatalf("paged = %+v, want 1 of 2", response.Pagination)
	}

	recorder = httptest.NewRecorder()
	fx.app.handleListAIAudits(recorder, listAIAuditsRequest(fx.ownerID, fx.projectID, "location_id="+fx.locationID.String()+"&crawl_id="+fx.crawlID.String()))
	response = decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.AIAudits[0].CrawlID != fx.crawlID.String() {
		t.Fatalf("location crawl = %+v, want the location audit for that crawl", response.AIAudits)
	}

	recorder = httptest.NewRecorder()
	fx.app.handleListAIAudits(recorder, listAIAuditsRequest(fx.ownerID, fx.projectID, "location_id="+fx.emptyLocationID.String()+"&crawl_id="+fx.crawlID.String()))
	response = decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 0 || response.Pagination.Total != 0 {
		t.Fatalf("empty scope = %+v, want empty", response.AIAudits)
	}
}

func TestListAIAuditsLocationScopeSecurity(t *testing.T) {
	fx := newLocationAuditFixture(t)
	var foreign pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1,'foreign',27.7,85.3) RETURNING id`,
		fx.otherProjectID).Scan(&foreign); err != nil {
		t.Fatalf("create foreign location: %v", err)
	}

	for _, test := range []struct {
		name       string
		user       pgtype.UUID
		query      string
		wantStatus int
	}{
		{"invalid location id", fx.ownerID, "location_id=not-a-uuid", http.StatusBadRequest},
		{"unknown location", fx.ownerID, "location_id=00000000-0000-0000-0000-000000000000", http.StatusNotFound},
		{"foreign location", fx.ownerID, "location_id=" + foreign.String(), http.StatusNotFound},
		{"outsider", fx.outsiderID, "location_id=" + fx.locationID.String(), http.StatusNotFound},
		{"invalid status in scope", fx.ownerID, "location_id=" + fx.locationID.String() + "&status=bogus", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			fx.app.handleListAIAudits(recorder, listAIAuditsRequest(test.user, fx.projectID, test.query))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestGetAIAuditLocationFields(t *testing.T) {
	fx := newLocationAuditFixture(t)
	var locationAuditID, projectAuditID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits (project_id, location_id, status) VALUES ($1,$2,'completed') RETURNING id`,
		fx.projectID, fx.locationID).Scan(&locationAuditID); err != nil {
		t.Fatalf("insert location audit: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_audit_runs (audit_id, question_text, display_order, model_name, status, mentioned_target, mentioned_branch) VALUES ($1,'q',1,'m','success',TRUE,TRUE)`,
		locationAuditID); err != nil {
		t.Fatalf("insert location run: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits (project_id, crawl_id, status) VALUES ($1,$2,'completed') RETURNING id`,
		fx.projectID, fx.crawlID).Scan(&projectAuditID); err != nil {
		t.Fatalf("insert project audit: %v", err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_audit_runs (audit_id, question_text, display_order, model_name, status) VALUES ($1,'q',1,'m','success')`,
		projectAuditID); err != nil {
		t.Fatalf("insert project run: %v", err)
	}

	getRequest := func(auditID pgtype.UUID) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/ai-audits/"+auditID.String(), nil)
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("auditID", auditID.String())
		ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
		return request.WithContext(withPrincipal(ctx, Principal{User: sqlc.User{ID: fx.ownerID}}))
	}

	recorder := httptest.NewRecorder()
	fx.app.handleGetAIAudit(recorder, getRequest(locationAuditID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var located aiAuditResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &located); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if located.LocationID != fx.locationID.String() {
		t.Fatalf("location_id = %q, want %q", located.LocationID, fx.locationID.String())
	}
	if len(located.Runs) != 1 || located.Runs[0].MentionedBranch == nil || !*located.Runs[0].MentionedBranch {
		t.Fatalf("runs = %+v, want mentioned_branch true", located.Runs)
	}

	recorder = httptest.NewRecorder()
	fx.app.handleGetAIAudit(recorder, getRequest(projectAuditID))
	var historic aiAuditResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &historic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if historic.LocationID != "" {
		t.Fatalf("location_id = %q, want absent for historic project audits", historic.LocationID)
	}
	if len(historic.Runs) != 1 || historic.Runs[0].MentionedBranch != nil {
		t.Fatalf("runs = %+v, want mentioned_branch unknown for historic runs", historic.Runs)
	}
}
