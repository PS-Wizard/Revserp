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
	"github.com/ps-wizard/revserp/internal/gsc"
)

// newCMSApprovalTestPool connects to an explicit disposable test database
// only. It never falls back to DATABASE_URL, so these tests cannot mutate a
// development or production database.
func newCMSApprovalTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("CMS_APPROVAL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CMS_APPROVAL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("cms approval test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"public.project_cms_connections", "public.ai_cms_approvals"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("cms approval schema is not migrated in the test database (missing %s)", table)
		}
	}
	return sqlc.New(pool), pool, ctx
}

type cmsApprovalFixture struct {
	app            *App
	queries        *sqlc.Queries
	pool           *pgxpool.Pool
	ctx            context.Context
	orgID          pgtype.UUID
	projectID      pgtype.UUID
	initiatorID    pgtype.UUID
	memberID       pgtype.UUID
	conversationID pgtype.UUID
}

func newCMSApprovalFixture(t *testing.T) cmsApprovalFixture {
	t.Helper()
	queries, pool, ctx := newCMSApprovalTestPool(t)
	name := fmt.Sprintf("cms-approval-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('cms-approval-test', $1, $2) RETURNING id`, name+role, name+role+"@example.com").Scan(&userID); err != nil {
			t.Fatalf("create user: %v", err)
		}
		if role != "outsider" {
			if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,$3)`, orgID, userID, role); err != nil {
				t.Fatalf("add member: %v", err)
			}
		}
		return userID
	}
	initiatorID := newUser("owner")
	memberID := newUser("member")
	_ = newUser("outsider")
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'cms-approval-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var conversationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_conversations (project_id, created_by_user_id, title) VALUES ($1,$2,'t') RETURNING id`, projectID, initiatorID).Scan(&conversationID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE auth_provider = 'cms-approval-test' AND auth_subject LIKE $1`, name+"%")
	})
	app := &App{
		DB:         pool,
		Queries:    queries,
		Config:     config.Config{GoogleTokenEncryptionSecret: runeTestEncryptionSecret},
		GSCService: gsc.NewService("", "", "", runeTestEncryptionSecret, 1<<20),
	}
	return cmsApprovalFixture{app: app, queries: queries, pool: pool, ctx: ctx, orgID: orgID, projectID: projectID, initiatorID: initiatorID, memberID: memberID, conversationID: conversationID}
}

// seedWaitingApproval creates a turn in the given status with one pending
// approval, mirroring what the worker persists before it waits.
func seedWaitingApproval(t *testing.T, f cmsApprovalFixture, turnStatus string) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	var turnID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,output_started_at) VALUES($1,$2,$3,'none','none','m','v',$4,decode(repeat('00',32),'hex'),now()) RETURNING id`,
		f.conversationID, f.initiatorID, turnStatus, fmt.Sprintf("seed-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','working')`, turnID); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	var approvalID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_cms_approvals(turn_id,tool_call_id,tool_name,provider,target,before_text,after_text,snapshot,proposed_args,connection_revision,status) VALUES($1,'call-1','cms__create_record','rune','posts','','{}','{}','{"a":1}','11111111-1111-1111-1111-111111111111','pending') RETURNING id`, turnID).Scan(&approvalID); err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-1','cms__create_record','{"a":1}','awaiting')`, turnID); err != nil {
		t.Fatalf("seed tool call: %v", err)
	}
	return turnID, approvalID
}

func approvalHandlerRequest(t *testing.T, method, conversationID, approvalID, body string, userID pgtype.UUID) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/ai/conversations/"+conversationID, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("conversationID", conversationID)
	if approvalID != "" {
		routeContext.URLParams.Add("approvalID", approvalID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

func decodeApprovalBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode approval response: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestCMSApprovalDecisionApproveKeepsWorkerRunning(t *testing.T) {
	f := newCMSApprovalFixture(t)
	turnID, approvalID := seedWaitingApproval(t, f, "running")
	rec := httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("decide: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeApprovalBody(t, rec)
	approval, _ := body["approval"].(map[string]any)
	if approval["status"] != "approved" || body["turn_id"] != turnID.String() {
		t.Fatalf("decision body = %v", body)
	}
	if _, ok := approval["decided_at"]; !ok {
		t.Fatalf("approved approval missing decided_at: %v", approval)
	}
	var turnStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "running" {
		t.Fatalf("turn status = %q, want running: the waiting worker continues its own run", turnStatus)
	}
	var decided int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'approval_decided'`, turnID).Scan(&decided); err != nil {
		t.Fatal(err)
	}
	if decided != 1 {
		t.Fatal("approval_decided event not persisted for replay")
	}

	// Repeating the same decision is idempotent and never changes the turn.
	rec = httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate decide: %d (%s)", rec.Code, rec.Body.String())
	}
	// The opposite decision conflicts.
	rec = httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting decide: %d, want 409", rec.Code)
	}
}

func TestCMSApprovalDecisionRejectAndAuth(t *testing.T) {
	f := newCMSApprovalFixture(t)
	turnID, approvalID := seedWaitingApproval(t, f, "running")

	// Non-initiator members fail closed.
	rec := httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject"}`, f.memberID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member decide: %d, want 403", rec.Code)
	}
	// Unknown decision values are rejected before touching state.
	rec = httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"maybe"}`, f.initiatorID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("reject: %d (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeApprovalBody(t, rec); body["approval"].(map[string]any)["status"] != "rejected" {
		t.Fatalf("reject body = %v", body)
	}
	var turnStatus, approvalStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_cms_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "running" || approvalStatus != "rejected" {
		t.Fatalf("turn=%q approval=%q, want running/rejected", turnStatus, approvalStatus)
	}
}

func TestCMSApprovalDecisionStaleTurn(t *testing.T) {
	f := newCMSApprovalFixture(t)
	_, approvalID := seedWaitingApproval(t, f, "queued")
	rec := httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale decide: %d, want 409", rec.Code)
	}
	var approvalStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_cms_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "pending" {
		t.Fatalf("stale decision mutated approval to %q", approvalStatus)
	}
}

func TestCMSApprovalListShowsHistory(t *testing.T) {
	f := newCMSApprovalFixture(t)
	turnID, _ := seedWaitingApproval(t, f, "waiting_for_user")
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_cms_approvals(turn_id,tool_call_id,tool_name,provider,status,decided_at,summary) VALUES($1,'call-0','cms__update_record','rune','rejected',now(),'no')`, turnID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.app.handleListCMSApprovals(rec, approvalHandlerRequest(t, http.MethodGet, f.conversationID.String(), "", "", f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals []map[string]any `json:"approvals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Approvals) != 2 {
		t.Fatalf("approvals = %d, want pending plus decision history", len(body.Approvals))
	}
	// Non-members see nothing.
	rec = httptest.NewRecorder()
	var outsiderID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM users WHERE auth_provider = 'cms-approval-test' AND auth_subject LIKE '%outsider'`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	f.app.handleListCMSApprovals(rec, approvalHandlerRequest(t, http.MethodGet, f.conversationID.String(), "", "", outsiderID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outsider list: %d, want 404", rec.Code)
	}
}

func TestWaitingTurnBlocksNewTurn(t *testing.T) {
	f := newCMSApprovalFixture(t)
	seedWaitingApproval(t, f, "waiting_for_user")
	busy, err := f.queries.HasActiveAITurnForConversation(f.ctx, f.conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatal("waiting turn does not count as active")
	}
	// A fresh message is rejected while the turn waits.
	req := httptest.NewRequest(http.MethodPost, "/ai/conversations/"+f.conversationID.String(), strings.NewReader(`{"content":"hello","reasoning_effort":"none","client_request_id":"busy-1"}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("conversationID", f.conversationID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	rec := httptest.NewRecorder()
	f.app.handleSubmitAITurn(rec, req.WithContext(ctx))
	if rec.Code != http.StatusConflict {
		t.Fatalf("submit while waiting: %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

func TestCancelWaitingInvalidatesApprovals(t *testing.T) {
	f := newCMSApprovalFixture(t)
	turnID, approvalID := seedWaitingApproval(t, f, "waiting_for_user")
	req := httptest.NewRequest(http.MethodPost, "/ai/turns/"+turnID.String()+"/cancel", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("turnID", turnID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	rec := httptest.NewRecorder()
	f.app.handleCancelAITurn(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel waiting: %d (%s)", rec.Code, rec.Body.String())
	}
	var turnStatus, approvalStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_cms_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "stopped" || approvalStatus != "invalidated" {
		t.Fatalf("turn=%q approval=%q, want stopped/invalidated", turnStatus, approvalStatus)
	}
	// A late decision cannot revive the stopped turn.
	rec = httptest.NewRecorder()
	f.app.handleDecideCMSApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("late decide: %d, want 409", rec.Code)
	}
}

func TestCMSConnectReplaceInvalidatesStaleApprovals(t *testing.T) {
	f := newCMSApprovalFixture(t)
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		closed := false
		return &fakeRuneSession{tools: []RuneTool{{Name: "list_records", Description: "d", InputSchema: json.RawMessage(`{}`)}}, closed: &closed}, nil
	}
	f.app.CMSConnect = func(ctx context.Context, provider CMSProvider, endpoint, token string) (RuneSession, error) {
		closed := false
		return &fakeRuneSession{tools: []RuneTool{{Name: "wp__get_content", Description: "d", InputSchema: json.RawMessage(`{}`)}}, closed: &closed}, nil
	}
	// Seed a rune connection the legacy way plus a waiting approval on it.
	rec := httptest.NewRecorder()
	f.app.handleRuneConnect(rec, runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://rune.example/mcp","bearer_token":"rune-secret-token"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed rune connect: %d (%s)", rec.Code, rec.Body.String())
	}
	turnID, approvalID := seedWaitingApproval(t, f, "waiting_for_user")

	// Replace with WordPress: revision rotates, provider switches, the stale
	// approval is invalidated and the waiting turn fails loudly.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/projects/"+f.projectID.String()+"/cms/connect", strings.NewReader(`{"provider":"wordpress","endpoint_url":"https://wp.example/wp-json","bearer_token":"wp-secret"}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", f.projectID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	f.app.handleCMSConnect(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("wordpress replace: %d (%s)", rec.Code, rec.Body.String())
	}
	var provider string
	if err := f.pool.QueryRow(f.ctx, `SELECT provider FROM project_cms_connections WHERE project_id = $1`, f.projectID).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if provider != "wordpress" {
		t.Fatalf("provider = %q, want exactly one wordpress connection", provider)
	}
	var approvalStatus, turnStatus, errorCode string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_cms_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status, COALESCE(error_code,'') FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus, &errorCode); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "invalidated" || turnStatus != "failed" || errorCode != "cms_connection_changed" {
		t.Fatalf("approval=%q turn=%q code=%q, want invalidated/failed/cms_connection_changed", approvalStatus, turnStatus, errorCode)
	}
	// The legacy rune surface no longer sees a connection.
	statusRec := httptest.NewRecorder()
	f.app.handleRuneStatus(statusRec, runeHandlerRequest(t, http.MethodGet, f.projectID.String(), "", f.initiatorID))
	var status map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["connected"] != false {
		t.Fatalf("rune status after wordpress replace = %v", status)
	}
}

func TestCMSConnectValidationAndWordPressUnavailable(t *testing.T) {
	f := newCMSApprovalFixture(t)
	newReq := func(body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/projects/"+f.projectID.String()+"/cms/connect", strings.NewReader(body))
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("projectID", f.projectID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
		ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
		return req.WithContext(ctx)
	}
	for _, body := range []string{
		`{"provider":"drupal","endpoint_url":"https://x.example","bearer_token":"t"}`,
		`{"provider":"rune","endpoint_url":"https://rune.example/mcp","bearer_token":"t","extra":1}`,
		`{"provider":"","endpoint_url":"https://rune.example/mcp","bearer_token":"t"}`,
	} {
		rec := httptest.NewRecorder()
		f.app.handleCMSConnect(rec, newReq(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: %d, want 400", body, rec.Code)
		}
	}
	// No WordPress connector wired degrades to 503, never to a hidden default.
	f.app.CMSConnect = nil
	rec := httptest.NewRecorder()
	f.app.handleCMSConnect(rec, newReq(`{"provider":"wordpress","endpoint_url":"https://wp.example/wp-json","bearer_token":"t"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("wordpress without connector: %d, want 503", rec.Code)
	}
}

func TestSubmitRecoveryFailsExecutingApprovals(t *testing.T) {
	f := newCMSApprovalFixture(t)
	var turnID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,attempt_count,claimed_by,lease_expires_at,output_started_at) VALUES($1,$2,'running','none','none','m','v',$3,decode(repeat('00',32),'hex'),2,'dead-worker',now()-interval '1 hour',now()) RETURNING id`,
		f.conversationID, f.initiatorID, fmt.Sprintf("recover-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed running turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','working')`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_cms_approvals(turn_id,tool_call_id,tool_name,provider,status) VALUES($1,'call-9','cms__update_record','rune','executing')`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-9','cms__update_record','{}','running')`, turnID); err != nil {
		t.Fatal(err)
	}
	if err := recoverExpiredAITurnsForConversation(f.ctx, f.queries, f.conversationID); err != nil {
		t.Fatalf("recover: %v", err)
	}
	var turnStatus, approvalStatus, toolStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_tool_calls WHERE turn_id = $1`, turnID).Scan(&toolStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "failed" || approvalStatus != "failed" || toolStatus != "failed" {
		t.Fatalf("turn=%q approval=%q tool=%q, want failed/failed/failed", turnStatus, approvalStatus, toolStatus)
	}
	var decided, results int
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'approval_decided'), (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'tool_result')`, turnID).Scan(&decided, &results); err != nil {
		t.Fatal(err)
	}
	if decided != 1 || results != 1 {
		t.Fatalf("replay events decided=%d results=%d, want 1/1", decided, results)
	}
}
