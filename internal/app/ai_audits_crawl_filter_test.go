package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// aiAuditListFixture wires a real DB-backed app plus an owner user, a project
// and two crawls. Skipped when no test database is available.
func aiAuditListFixture(t *testing.T) (*App, *sqlc.Queries, *pgxpool.Pool, context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	name := fmt.Sprintf("ai-audit-list-test-%d", time.Now().UnixNano())
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
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'ai-audit-list-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var crawlAID, crawlBID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, projectID).Scan(&crawlAID); err != nil {
		t.Fatalf("create crawl A: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, projectID).Scan(&crawlBID); err != nil {
		t.Fatalf("create crawl B: %v", err)
	}
	return &App{DB: pool, Queries: queries}, queries, pool, ctx, userID, projectID, crawlAID, crawlBID
}

func listAIAuditsRequest(userID, projectID pgtype.UUID, query string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/projects/"+projectID.String()+"/ai-audits?"+query, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", projectID.String())
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return request.WithContext(ctx)
}

type aiAuditListResponse struct {
	AIAudits   []aiAuditResponse `json:"ai_audits"`
	Pagination struct {
		Limit  int32 `json:"limit"`
		Offset int32 `json:"offset"`
		Count  int32 `json:"count"`
		Total  int64 `json:"total"`
	} `json:"pagination"`
}

func decodeAIAuditList(t *testing.T, recorder *httptest.ResponseRecorder) aiAuditListResponse {
	t.Helper()
	var response aiAuditListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	return response
}

// The crawl filter must return the latest audit for that crawl even when it
// sits outside the first page of the unfiltered list.
func TestListAIAuditsCrawlFilterFindsOlderAudit(t *testing.T) {
	app, queries, pool, ctx, userID, projectID, crawlAID, crawlBID := aiAuditListFixture(t)

	var oldAuditID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status, created_at) VALUES ($1,$2,'completed', now() - interval '2 hour') RETURNING id`, projectID, crawlAID).Scan(&oldAuditID); err != nil {
		t.Fatalf("insert old audit: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status, created_at) VALUES ($1,$2,'completed', now())`, projectID, crawlBID); err != nil {
			t.Fatalf("insert newer audit: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "limit=1&offset=0&crawl_id="+crawlAID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.AIAudits[0].ID != oldAuditID.String() {
		t.Fatalf("audits = %+v, want the older crawl A audit %s", response.AIAudits, oldAuditID.String())
	}
	if response.AIAudits[0].CrawlID != crawlAID.String() {
		t.Fatalf("crawl_id = %q, want %q", response.AIAudits[0].CrawlID, crawlAID.String())
	}
	if response.Pagination.Total != 1 || response.Pagination.Count != 1 {
		t.Fatalf("pagination = %+v, want total 1 count 1", response.Pagination)
	}

	// Without the filter the same page holds a newer audit instead.
	recorder = httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "limit=1&offset=0"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unfiltered status = %d, want 200", recorder.Code)
	}
	unfiltered := decodeAIAuditList(t, recorder)
	if len(unfiltered.AIAudits) != 1 || unfiltered.AIAudits[0].ID == oldAuditID.String() {
		t.Fatalf("unfiltered first page = %+v, want a newer audit", unfiltered.AIAudits)
	}
	if unfiltered.Pagination.Total != 4 {
		t.Fatalf("unfiltered total = %d, want 4", unfiltered.Pagination.Total)
	}
	_ = queries
}

func TestListAIAuditsCrawlFilterEmpty(t *testing.T) {
	app, _, _, _, userID, projectID, _, crawlBID := aiAuditListFixture(t)

	recorder := httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "crawl_id="+crawlBID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAIAuditList(t, recorder)
	if response.AIAudits == nil || len(response.AIAudits) != 0 {
		t.Fatalf("audits = %+v, want empty list", response.AIAudits)
	}
	if response.Pagination.Total != 0 || response.Pagination.Count != 0 {
		t.Fatalf("pagination = %+v, want total 0 count 0", response.Pagination)
	}
}

func TestListAIAuditsCrawlFilterInvalidUUID(t *testing.T) {
	app, _, _, _, userID, projectID, _, _ := aiAuditListFixture(t)

	recorder := httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "crawl_id=not-a-uuid"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
}

// Tenancy: outsiders get 404 exactly as before, and a crawl from another
// project yields an empty list instead of leaking the audit.
func TestListAIAuditsCrawlFilterTenancy(t *testing.T) {
	app, _, pool, ctx, _, projectID, crawlAID, _ := aiAuditListFixture(t)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status) VALUES ($1,$2,'completed')`, projectID, crawlAID); err != nil {
		t.Fatalf("insert audit: %v", err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`,
		fmt.Sprintf("ai-audit-outsider-%d", time.Now().UnixNano()), "outsider@example.com").Scan(&outsiderID); err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", outsiderID)
	})
	recorder := httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(outsiderID, projectID, "crawl_id="+crawlAID.String()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}

	var otherProjectID, otherCrawlID pgtype.UUID
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT organization_id FROM projects WHERE id = $1`, projectID).Scan(&orgID); err != nil {
		t.Fatalf("lookup org: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'other','https://other.example.com') RETURNING id`, orgID).Scan(&otherProjectID); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, otherProjectID).Scan(&otherCrawlID); err != nil {
		t.Fatalf("create other crawl: %v", err)
	}
	var memberID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM organization_members WHERE org_id = $1 LIMIT 1`, orgID).Scan(&memberID); err != nil {
		t.Fatalf("lookup member: %v", err)
	}
	recorder = httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(memberID, projectID, "crawl_id="+otherCrawlID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("cross-project status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 0 {
		t.Fatalf("cross-project audits = %+v, want empty", response.AIAudits)
	}
}

// With both filters supplied the crawl filter wins; an invalid status is
// still rejected because the handler validates it independently.
func TestListAIAuditsCrawlFilterWithStatus(t *testing.T) {
	app, _, pool, ctx, userID, projectID, crawlAID, _ := aiAuditListFixture(t)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status) VALUES ($1,$2,'failed')`, projectID, crawlAID); err != nil {
		t.Fatalf("insert audit: %v", err)
	}

	recorder := httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "status=completed&crawl_id="+crawlAID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.AIAudits[0].Status != "failed" {
		t.Fatalf("audits = %+v, want the crawl audit regardless of status filter", response.AIAudits)
	}

	recorder = httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "status=bogus&crawl_id="+crawlAID.String()))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid status code = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
}

// Without crawl_id the existing status filter and pagination behavior is
// unchanged.
func TestListAIAuditsWithoutCrawlFilterKeepsStatusAndPagination(t *testing.T) {
	app, _, pool, ctx, userID, projectID, crawlAID, crawlBID := aiAuditListFixture(t)
	for _, row := range []struct {
		crawl  pgtype.UUID
		status string
	}{
		{crawlAID, "completed"},
		{crawlBID, "failed"},
		{crawlBID, "completed"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO ai_audits (project_id, crawl_id, status) VALUES ($1,$2,$3)`, projectID, row.crawl, row.status); err != nil {
			t.Fatalf("insert audit: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	app.handleListAIAudits(recorder, listAIAuditsRequest(userID, projectID, "status=failed&limit=1&offset=0"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAIAuditList(t, recorder)
	if len(response.AIAudits) != 1 || response.AIAudits[0].Status != "failed" {
		t.Fatalf("audits = %+v, want the single failed audit", response.AIAudits)
	}
	if response.Pagination.Total != 1 || response.Pagination.Limit != 1 || response.Pagination.Offset != 0 {
		t.Fatalf("pagination = %+v, want total 1 limit 1 offset 0", response.Pagination)
	}
}
