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
	"github.com/ps-wizard/revserp/internal/mcpclient"
)

// newMCPApprovalTestPool connects to an explicit disposable test database
// only. It never falls back to DATABASE_URL, so these tests cannot mutate a
// development or production database.
func newMCPApprovalTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("MCP_APPROVAL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCP_APPROVAL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("mcp approval test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := internaldb.EnsureMCPMarketplaceTestSchema(ctx, pool); err != nil {
		t.Skipf("mcp marketplace test schema not available: %v", err)
	}
	for _, table := range []string{"public.project_mcp_connections", "public.ai_mcp_approvals"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("cms approval schema is not migrated in the test database (missing %s)", table)
		}
	}
	return sqlc.New(pool), pool, ctx
}

type mcpApprovalFixture struct {
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

func newMCPApprovalFixture(t *testing.T) mcpApprovalFixture {
	t.Helper()
	queries, pool, ctx := newMCPApprovalTestPool(t)
	name := fmt.Sprintf("cms-approval-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('mcp-approval-test', $1, $2) RETURNING id`, name+role, name+role+"@example.com").Scan(&userID); err != nil {
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
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'mcp-approval-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
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
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE auth_provider = 'mcp-approval-test' AND auth_subject LIKE $1`, name+"%")
	})
	app := &App{
		DB:         pool,
		Queries:    queries,
		Config:     config.Config{GoogleTokenEncryptionSecret: mcpTestEncryptionSecret},
		GSCService: gsc.NewService("", "", "", mcpTestEncryptionSecret, 1<<20),
	}
	return mcpApprovalFixture{app: app, queries: queries, pool: pool, ctx: ctx, orgID: orgID, projectID: projectID, initiatorID: initiatorID, memberID: memberID, conversationID: conversationID}
}

// seedWaitingApproval creates a turn in the given status with one pending
// approval, mirroring what the worker persists before it waits: the exact
// connection, remote tool, immutable args, and schema digest the decision
// handler validates before approving.
func seedWaitingApproval(t *testing.T, f mcpApprovalFixture, turnStatus string) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	var turnID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,output_started_at) VALUES($1,$2,$3,'none','none','m','v',$4,decode(repeat('00',32),'hex'),now()) RETURNING id`,
		f.conversationID, f.initiatorID, turnStatus, fmt.Sprintf("seed-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','working')`, turnID); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO project_mcp_connections(project_id,name,service,endpoint_url,encrypted_token,revision,tools,last_checked_at) VALUES($1,'Seed-' || substr(md5(random()::text), 1, 8),'custom','https://seed.example/mcp','enc:seed','11111111-1111-1111-1111-111111111111','[{"name":"create_record","description":"d","input_schema":{"type":"object"}}]',now()) RETURNING id`, f.projectID).Scan(&connectionID); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	var approvalID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_mcp_approvals(turn_id,tool_call_id,tool_name,provider,service,connection_id,connection_name,remote_tool_name,target,before_text,after_text,snapshot,proposed_args,connection_revision,status) VALUES($1,'call-1','mcp_seed_alias','custom','custom',$2,'Seed','create_record','posts','','{}','{}','{"a":1}','11111111-1111-1111-1111-111111111111','pending') RETURNING id`, turnID, connectionID).Scan(&approvalID); err != nil {
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

func TestMCPApprovalDecisionApproveKeepsWorkerRunning(t *testing.T) {
	f := newMCPApprovalFixture(t)
	turnID, approvalID := seedWaitingApproval(t, f, "running")
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
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
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate decide: %d (%s)", rec.Code, rec.Body.String())
	}
	// The opposite decision conflicts.
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting decide: %d, want 409", rec.Code)
	}
}

func TestMCPApprovalDecisionRejectAndAuth(t *testing.T) {
	f := newMCPApprovalFixture(t)
	turnID, approvalID := seedWaitingApproval(t, f, "running")

	// Non-initiator members fail closed.
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject"}`, f.memberID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member decide: %d, want 403", rec.Code)
	}
	// Unknown decision values are rejected before touching state.
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"maybe"}`, f.initiatorID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject"}`, f.initiatorID))
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
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "running" || approvalStatus != "rejected" {
		t.Fatalf("turn=%q approval=%q, want running/rejected", turnStatus, approvalStatus)
	}
}

func TestMCPApprovalDecisionStaleTurn(t *testing.T) {
	f := newMCPApprovalFixture(t)
	_, approvalID := seedWaitingApproval(t, f, "queued")
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale decide: %d, want 409", rec.Code)
	}
	var approvalStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "pending" {
		t.Fatalf("stale decision mutated approval to %q", approvalStatus)
	}
}

func TestMCPApprovalListShowsHistory(t *testing.T) {
	f := newMCPApprovalFixture(t)
	turnID, _ := seedWaitingApproval(t, f, "waiting_for_user")
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_mcp_approvals(turn_id,tool_call_id,tool_name,provider,status,decided_at,summary) VALUES($1,'call-0','cms__update_record','rune','rejected',now(),'no')`, turnID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.app.handleListMCPApprovals(rec, approvalHandlerRequest(t, http.MethodGet, f.conversationID.String(), "", "", f.initiatorID))
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
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM users WHERE auth_provider = 'mcp-approval-test' AND auth_subject LIKE '%outsider'`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	f.app.handleListMCPApprovals(rec, approvalHandlerRequest(t, http.MethodGet, f.conversationID.String(), "", "", outsiderID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outsider list: %d, want 404", rec.Code)
	}
}

func TestWaitingTurnBlocksNewTurn(t *testing.T) {
	f := newMCPApprovalFixture(t)
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
	f := newMCPApprovalFixture(t)
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
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "stopped" || approvalStatus != "invalidated" {
		t.Fatalf("turn=%q approval=%q, want stopped/invalidated", turnStatus, approvalStatus)
	}
	// A late decision cannot revive the stopped turn.
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("late decide: %d, want 409", rec.Code)
	}
}

func TestMCPReplaceInvalidatesStaleApprovals(t *testing.T) {
	f := newMCPApprovalFixture(t)
	f.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		closed := false
		return &fakeMCPSession{tools: []mcpclient.Tool{{Name: "list_content", Description: "d", InputSchema: json.RawMessage(`{}`)}}, closed: &closed}, nil
	}
	// Seed one connection plus a waiting approval on it.
	rec := httptest.NewRecorder()
	f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"name":"WordPress","service":"wordpress","endpoint_url":"https://wp.example/mcp","bearer_token":"wp-secret-token"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", rec.Code, rec.Body.String())
	}
	turnID, approvalID := seedWaitingApproval(t, f, "waiting_for_user")
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT c.id FROM project_mcp_connections AS c JOIN ai_mcp_approvals AS a ON a.connection_id = c.id WHERE a.id = $1`, approvalID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	// Replace the endpoint: revision rotates, the stale approval is
	// invalidated and the waiting turn fails loudly.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/projects/"+f.projectID.String()+"/mcp/connections/"+connectionID.String(), strings.NewReader(`{"endpoint_url":"https://wp2.example/mcp","bearer_token":"wp-secret-2"}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", f.projectID.String())
	routeContext.URLParams.Add("connectionID", connectionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	f.app.handleMCPPatchConnection(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d (%s)", rec.Code, rec.Body.String())
	}
	var approvalStatus, turnStatus, errorCode string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status, COALESCE(error_code,'') FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus, &errorCode); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "invalidated" || turnStatus != "failed" || errorCode != "cms_connection_changed" {
		t.Fatalf("approval=%q turn=%q code=%q, want invalidated/failed/cms_connection_changed", approvalStatus, turnStatus, errorCode)
	}
}

func TestMCPConnectValidationAndUnavailable(t *testing.T) {
	f := newMCPApprovalFixture(t)
	f.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		return &fakeMCPSession{}, nil
	}
	for _, body := range []string{
		`{"name":"x","service":"drupal","endpoint_url":"https://x.example","bearer_token":"t"}`,
		`{"name":"x","service":"wordpress","endpoint_url":"https://x.example","bearer_token":"t","extra":1}`,
		`{"name":"","service":"wordpress","endpoint_url":"https://x.example","bearer_token":"t"}`,
		`{"name":"x","service":"wordpress","endpoint_url":"ftp://x.example","bearer_token":"t"}`,
	} {
		rec := httptest.NewRecorder()
		f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), body, f.initiatorID))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: %d, want 400", body, rec.Code)
		}
	}
	// No connector wired degrades to 503, never to a hidden default.
	f.app.MCPConnect = nil
	rec := httptest.NewRecorder()
	f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"name":"x","service":"wordpress","endpoint_url":"https://x.example","bearer_token":"t"}`, f.initiatorID))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without connector: %d, want 503", rec.Code)
	}
}

func TestMCPAlwaysAllowSavesExactRuleAtomically(t *testing.T) {
	f := newMCPApprovalFixture(t)
	f.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		return &fakeMCPSession{tools: []mcpclient.Tool{{Name: "update_content", Description: "d", InputSchema: json.RawMessage(`{}`)}}}, nil
	}
	rec := httptest.NewRecorder()
	f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"name":"WordPress","service":"wordpress","endpoint_url":"https://wp.example/mcp","bearer_token":"s"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", rec.Code, rec.Body.String())
	}
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM project_mcp_connections WHERE project_id = $1`, f.projectID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	// Seed the approval the worker would persist: exact connection, remote
	// tool, and immutable args.
	var turnID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash) VALUES($1,$2,'running','none','none','m','v',$3,decode(repeat('00',32),'hex')) RETURNING id`,
		f.conversationID, f.initiatorID, fmt.Sprintf("always-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','w')`, turnID); err != nil {
		t.Fatal(err)
	}
	schemaDigest, err := mcpCanonicalSchemaDigest(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var approvalID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_mcp_approvals(turn_id,tool_call_id,tool_name,provider,service,connection_id,connection_name,remote_tool_name,schema_digest,proposed_args,connection_revision,status) VALUES($1,'call-1','mcp_alias','wordpress','wordpress',$2,'WordPress','update_content',$3,'{"a":1}',(SELECT revision FROM project_mcp_connections WHERE id=$2),'pending') RETURNING id`, turnID, connectionID, schemaDigest).Scan(&approvalID); err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve","always_allow":true}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("always allow: %d (%s)", rec.Code, rec.Body.String())
	}
	var permission, approvalStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT permission FROM project_mcp_tool_permissions WHERE connection_id = $1 AND tool_name = 'update_content'`, connectionID).Scan(&permission); err != nil {
		t.Fatalf("saved rule: %v", err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if permission != "allow" || approvalStatus != "approved" {
		t.Fatalf("permission=%q approval=%q, want allow/approved in one transaction", permission, approvalStatus)
	}
	// always_allow is valid only with approve.
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"reject","always_allow":true}`, f.initiatorID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reject with always_allow: %d, want 400", rec.Code)
	}
}

func TestSubmitRecoveryFailsExecutingApprovals(t *testing.T) {
	f := newMCPApprovalFixture(t)
	var turnID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,attempt_count,claimed_by,lease_expires_at,output_started_at) VALUES($1,$2,'running','none','none','m','v',$3,decode(repeat('00',32),'hex'),2,'dead-worker',now()-interval '1 hour',now()) RETURNING id`,
		f.conversationID, f.initiatorID, fmt.Sprintf("recover-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed running turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','working')`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_mcp_approvals(turn_id,tool_call_id,tool_name,provider,status) VALUES($1,'call-9','cms__update_record','rune','executing')`, turnID); err != nil {
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
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvalStatus); err != nil {
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

func turnDetailRequest(t *testing.T, turnID string, userID pgtype.UUID) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ai/turns/"+turnID, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("turnID", turnID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

// TestTurnSnapshotCursorCoversCatchUp is the snapshot/cursor contract the
// frontend reload path depends on: one GET returns the turn, its messages,
// its current approvals, and the event cursor of exactly that committed
// snapshot, so subscribing after the cursor replays neither gaps nor
// duplicates.
func TestTurnSnapshotCursorCoversCatchUp(t *testing.T) {
	f := newMCPApprovalFixture(t)
	turnID, _ := seedWaitingApproval(t, f, "running")
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_turn_events(turn_id,event_type,payload) VALUES($1,'phase','{"phase":"thinking"}'),($1,'text_delta','{"text":"hi"}')`, turnID); err != nil {
		t.Fatal(err)
	}
	var maxBefore int64
	if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE(MAX(id),0) FROM ai_turn_events WHERE turn_id = $1`, turnID).Scan(&maxBefore); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.app.handleGetAITurn(rec, turnDetailRequest(t, turnID.String(), f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get turn: %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		ID          string           `json:"id"`
		Messages    []map[string]any `json:"messages"`
		Approvals   []map[string]any `json:"approvals"`
		EventCursor int64            `json:"event_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.EventCursor != maxBefore {
		t.Fatalf("event_cursor = %d, want the snapshot MAX(id) %d", body.EventCursor, maxBefore)
	}
	if len(body.Approvals) != 1 || len(body.Messages) == 0 {
		t.Fatalf("snapshot approvals=%d messages=%d, want the current cards with the text", len(body.Approvals), len(body.Messages))
	}
	// One event lands after the snapshot: catch-up from the cursor must
	// deliver exactly it, nothing replayed, nothing skipped.
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_turn_events(turn_id,event_type,payload) VALUES($1,'text_delta','{"text":"late"}')`, turnID); err != nil {
		t.Fatal(err)
	}
	catchUp, err := f.queries.ListAITurnEventsForUser(f.ctx, sqlc.ListAITurnEventsForUserParams{UserID: f.initiatorID, TurnID: turnID, AfterID: body.EventCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(catchUp) != 1 || catchUp[0].EventType != "text_delta" {
		t.Fatalf("catch-up after cursor = %+v, want exactly the late event", catchUp)
	}
	var payload map[string]any
	if err := json.Unmarshal(catchUp[0].Payload, &payload); err != nil || payload["text"] != "late" {
		t.Fatalf("catch-up payload = %s, want the late event", catchUp[0].Payload)
	}
}

// TestMCPPatchStaleRevisionCAS pins the replace compare-and-set: a write
// against a moved revision touches no row, so a concurrent replacement is
// never overwritten.
func TestMCPPatchStaleRevisionCAS(t *testing.T) {
	f := newMCPApprovalFixture(t)
	var id, revision pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO project_mcp_connections(project_id,name,service,endpoint_url,encrypted_token,revision,tools,last_checked_at) VALUES($1,'CAS','custom','https://cas.example/mcp','enc:x','11111111-1111-1111-1111-111111111111','[]',now()) RETURNING id, revision`, f.projectID).Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE project_mcp_connections SET revision = gen_random_uuid() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queries.ReplaceProjectMCPConnection(f.ctx, sqlc.ReplaceProjectMCPConnectionParams{
		ID: id, ProjectID: f.projectID, Name: "CAS", EndpointUrl: "https://evil.example/mcp",
		EncryptedToken: "enc:y", Tools: []byte(`[]`), Revision: revision,
	}); err == nil {
		t.Fatal("stale revision replace succeeded: concurrent replacement overwritten")
	}
	var endpoint string
	if err := f.pool.QueryRow(f.ctx, `SELECT endpoint_url FROM project_mcp_connections WHERE id = $1`, id).Scan(&endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://cas.example/mcp" {
		t.Fatalf("endpoint = %q after failed CAS", endpoint)
	}
}

// TestMCPApproveRejectsStaleRevision blocks an approve after the connection
// was replaced: the revision pinned at request time no longer matches.
func TestMCPApproveRejectsStaleRevision(t *testing.T) {
	f := newMCPApprovalFixture(t)
	_, approvalID := seedWaitingApproval(t, f, "running")
	if _, err := f.pool.Exec(f.ctx, `UPDATE project_mcp_connections SET revision = gen_random_uuid() WHERE project_id = $1`, f.projectID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale revision approve: %d, want 409", rec.Code)
	}
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("stale decision mutated approval to %q", status)
	}
}

// TestMCPApproveRejectsChangedSchema blocks an approve after rediscovery
// changed the exact tool's schema, even with the revision intact.
func TestMCPApproveRejectsChangedSchema(t *testing.T) {
	f := newMCPApprovalFixture(t)
	_, approvalID := seedWaitingApproval(t, f, "running")
	digest, err := mcpCanonicalSchemaDigest(json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE ai_mcp_approvals SET schema_digest = $2 WHERE id = $1`, approvalID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE project_mcp_connections SET tools = '[{"name":"create_record","description":"d","input_schema":{"type":"object","properties":{"new":{"type":"string"}}}}]' WHERE project_id = $1`, f.projectID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed schema approve: %d, want 409", rec.Code)
	}
}

// TestMCPApproveBlockedByDeny fails a plain approve closed when the rule
// is Deny, while an explicit always_allow on the fresh card re-grants the
// exact rule atomically with the decision.
func TestMCPApproveBlockedByDeny(t *testing.T) {
	f := newMCPApprovalFixture(t)
	_, approvalID := seedWaitingApproval(t, f, "running")
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT c.id FROM project_mcp_connections AS c JOIN ai_mcp_approvals AS a ON a.connection_id = c.id WHERE a.id = $1`, approvalID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'create_record', 'deny')`, connectionID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("denied approve: %d, want 409", rec.Code)
	}
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve","always_allow":true}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh always_allow: %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var permission, status string
	if err := f.pool.QueryRow(f.ctx, `SELECT permission FROM project_mcp_tool_permissions WHERE connection_id = $1 AND tool_name = 'create_record'`, connectionID).Scan(&permission); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if permission != "allow" || status != "approved" {
		t.Fatalf("rule=%q approval=%q, want allow/approved", permission, status)
	}
}

// TestMCPNonInitiatorReplayForbidden checks the initiator guard runs before
// the idempotent replay: a non-initiator cannot re-trigger a decision.
func TestMCPNonInitiatorReplayForbidden(t *testing.T) {
	f := newMCPApprovalFixture(t)
	_, approvalID := seedWaitingApproval(t, f, "running")
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("initiator approve: %d (%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.memberID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member replay: %d, want 403", rec.Code)
	}
}

// TestMCPPatchNameConflictChangesNothing proves a conflicting rename fails
// before the replacement: no revision bump, no endpoint swap, and no
// approval invalidation that would strand a changed connection.
func TestMCPPatchNameConflictChangesNothing(t *testing.T) {
	f := newMCPApprovalFixture(t)
	f.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		return &fakeMCPSession{tools: []mcpclient.Tool{{Name: "list_content", Description: "d", InputSchema: json.RawMessage(`{}`)}}}, nil
	}
	create := func(name string) {
		t.Helper()
		rec := httptest.NewRecorder()
		f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"name":"`+name+`","service":"wordpress","endpoint_url":"https://wp.example/mcp","bearer_token":"s"}`, f.initiatorID))
		if rec.Code != http.StatusOK {
			t.Fatalf("create %s: %d (%s)", name, rec.Code, rec.Body.String())
		}
	}
	create("Alpha")
	create("Beta")
	var alpha pgtype.UUID
	var endpoint, revision string
	if err := f.pool.QueryRow(f.ctx, `SELECT id, endpoint_url, revision::text FROM project_mcp_connections WHERE project_id = $1 AND name = 'Alpha'`, f.projectID).Scan(&alpha, &endpoint, &revision); err != nil {
		t.Fatal(err)
	}
	var turnID, approvalID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash) VALUES($1,$2,'waiting_for_user','none','none','m','v',$3,decode(repeat('00',32),'hex')) RETURNING id`, f.conversationID, f.initiatorID, fmt.Sprintf("conflict-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_mcp_approvals(turn_id,tool_call_id,tool_name,provider,service,connection_id,connection_name,remote_tool_name,status) VALUES($1,'call-c','mcp_alias','wordpress','wordpress',$2,'Alpha','list_content','pending') RETURNING id`, turnID, alpha).Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/projects/"+f.projectID.String()+"/mcp/connections/"+alpha.String(), strings.NewReader(`{"name":"Beta","endpoint_url":"https://wp2.example/mcp","bearer_token":"s2"}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", f.projectID.String())
	routeContext.URLParams.Add("connectionID", alpha.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	f.app.handleMCPPatchConnection(rec, req.WithContext(ctx))
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting rename: %d, want 409", rec.Code)
	}
	var afterEndpoint, afterRevision string
	var afterTools []byte
	var approvalStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT endpoint_url, revision::text, tools FROM project_mcp_connections WHERE id = $1`, alpha).Scan(&afterEndpoint, &afterRevision, &afterTools); err != nil {
		t.Fatal(err)
	}
	if afterEndpoint != endpoint || afterRevision != revision {
		t.Fatalf("endpoint=%q revision changed: a failed rename must not strand a replacement", afterEndpoint)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "pending" {
		t.Fatalf("approval = %q after failed replace, want pending: invalidation must roll back too", approvalStatus)
	}
}

func mcpPermissionsRequest(t *testing.T, f mcpApprovalFixture, connectionID pgtype.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/projects/"+f.projectID.String()+"/mcp/connections/"+connectionID.String()+"/permissions", strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", f.projectID.String())
	routeContext.URLParams.Add("connectionID", connectionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	rec := httptest.NewRecorder()
	f.app.handleMCPPutPermissions(rec, req.WithContext(ctx))
	return rec
}

// TestMCPPutValidatesCurrentCatalogue proves the permission batch locks the
// connection and validates the current discovered set: a concurrent /check
// that swapped the tools makes the stale names reject and the response
// render the current row, not the locked one.
func TestMCPPutValidatesCurrentCatalogue(t *testing.T) {
	f := newMCPApprovalFixture(t)
	f.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		return &fakeMCPSession{tools: []mcpclient.Tool{{Name: "update_content", Description: "d", InputSchema: json.RawMessage(`{}`)}}}, nil
	}
	rec := httptest.NewRecorder()
	f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"name":"WordPress","service":"wordpress","endpoint_url":"https://wp.example/mcp","bearer_token":"s"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", rec.Code, rec.Body.String())
	}
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM project_mcp_connections WHERE project_id = $1`, f.projectID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	// A concurrent /check swaps the catalogue out from under the saved row.
	if _, err := f.pool.Exec(f.ctx, `UPDATE project_mcp_connections SET tools = '[{"name":"fresh_tool","description":"d","input_schema":{}}]' WHERE id = $1`, connectionID); err != nil {
		t.Fatal(err)
	}
	rec = mcpPermissionsRequest(t, f, connectionID, `{"permissions":[{"tool_name":"update_content","permission":"allow"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stale name: %d, want 400", rec.Code)
	}
	rec = mcpPermissionsRequest(t, f, connectionID, `{"permissions":[{"tool_name":"fresh_tool","permission":"allow"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("current name: %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Connection struct {
			Tools []struct {
				Name       string `json:"name"`
				Permission string `json:"permission"`
			} `json:"tools"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Connection.Tools) != 1 || body.Connection.Tools[0].Name != "fresh_tool" || body.Connection.Tools[0].Permission != "allow" {
		t.Fatalf("rendered connection = %+v, want the current tools with policy", body.Connection.Tools)
	}
}

// TestMCPPutRejectsAllowForRestricted proves a platform-restricted tool is
// shown unavailable with its truthful reason and can never be allowed,
// while deny stays a valid explicit rule.
func TestMCPPutRejectsAllowForRestricted(t *testing.T) {
	f := newMCPApprovalFixture(t)
	f.app.MCPConnect = func(ctx context.Context, endpoint, token string) (MCPSession, error) {
		return &fakeMCPSession{tools: []mcpclient.Tool{
			{Name: "describe_tables", Description: "d", InputSchema: json.RawMessage(`{}`)},
			{Name: "update_content", Description: "d", InputSchema: json.RawMessage(`{}`)},
		}}, nil
	}
	rec := httptest.NewRecorder()
	f.app.handleMCPCreateConnection(rec, mcpHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"name":"WordPress","service":"wordpress","endpoint_url":"https://wp.example/mcp","bearer_token":"s"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", rec.Code, rec.Body.String())
	}
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM project_mcp_connections WHERE project_id = $1`, f.projectID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	rec = mcpPermissionsRequest(t, f, connectionID, `{"permissions":[{"tool_name":"describe_tables","permission":"allow"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("restricted allow: %d, want 400", rec.Code)
	}
	rec = mcpPermissionsRequest(t, f, connectionID, `{"permissions":[{"tool_name":"describe_tables","permission":"deny"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restricted deny: %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var listed struct {
		Connections []struct {
			Tools []struct {
				Name              string  `json:"name"`
				Available         bool    `json:"available"`
				UnavailableReason *string `json:"unavailable_reason"`
			} `json:"tools"`
		} `json:"connections"`
	}
	listRec := httptest.NewRecorder()
	f.app.handleMCPListConnections(listRec, mcpHandlerRequest(t, http.MethodGet, f.projectID.String(), "", f.initiatorID))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: %d", listRec.Code)
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Connections) != 1 {
		t.Fatalf("connections = %+v", listed.Connections)
	}
	for _, tool := range listed.Connections[0].Tools {
		if tool.Name != "describe_tables" {
			continue
		}
		if tool.Available || tool.UnavailableReason == nil || *tool.UnavailableReason == "" {
			t.Fatalf("restricted tool = %+v, want available:false with reason", tool)
		}
		return
	}
	t.Fatal("restricted tool missing from the connection shape")
}

// TestMCPAlwaysAllowRequiresConnectionOwner keeps the permission-write
// boundary: a turn initiator without the owner role gets 403 with no policy
// mutation, while the plain approve still succeeds initiator-only.
func TestMCPAlwaysAllowRequiresConnectionOwner(t *testing.T) {
	f := newMCPApprovalFixture(t)
	// A member-role initiator owns this turn but not the permission surface.
	var connectionID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO project_mcp_connections(project_id,name,service,endpoint_url,encrypted_token,revision,tools,last_checked_at) VALUES($1,'Member','custom','https://member.example/mcp','enc:m','33333333-3333-3333-3333-333333333333','[{"name":"create_record","description":"d","input_schema":{"type":"object"}}]',now()) RETURNING id`, f.projectID).Scan(&connectionID); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	var turnID, approvalID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash) VALUES($1,$2,'running','none','none','m','v',$3,decode(repeat('00',32),'hex')) RETURNING id`,
		f.conversationID, f.memberID, fmt.Sprintf("member-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','w')`, turnID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_mcp_approvals(turn_id,tool_call_id,tool_name,provider,service,connection_id,connection_name,remote_tool_name,proposed_args,connection_revision,status) VALUES($1,'call-m','mcp_member','custom','custom',$2,'Member','create_record','{"a":1}','33333333-3333-3333-3333-333333333333','pending') RETURNING id`, turnID, connectionID).Scan(&approvalID); err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	rec := httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve","always_allow":true}`, f.memberID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member always_allow: %d, want 403", rec.Code)
	}
	var rules int
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM project_mcp_tool_permissions WHERE connection_id = $1`, connectionID).Scan(&rules); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if rules != 0 || status != "pending" {
		t.Fatalf("rules=%d approval=%q: a refused AlwaysAllow must mutate nothing", rules, status)
	}
	// The plain approve stays initiator-only and succeeds.
	rec = httptest.NewRecorder()
	f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve"}`, f.memberID))
	if rec.Code != http.StatusOK {
		t.Fatalf("member plain approve: %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

// TestConcurrentAlwaysAllowVsDeny races an AlwaysAllow decision against a
// Deny permission edit on the same connection and tool with a bounded
// completion: no deadlock, no 500, and a consistent end state. A revoked
// pending call is never resurrected into a fresh rule: the decision writes
// its rule only after its compare-and-set wins.
func TestConcurrentAlwaysAllowVsDeny(t *testing.T) {
	f := newMCPApprovalFixture(t)
	for round := 0; round < 3; round++ {
		_, approvalID := seedWaitingApproval(t, f, "running")
		var connectionID pgtype.UUID
		if err := f.pool.QueryRow(f.ctx, `SELECT c.id FROM project_mcp_connections AS c JOIN ai_mcp_approvals AS a ON a.connection_id = c.id WHERE a.id = $1`, approvalID).Scan(&connectionID); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		type outcome struct {
			code int
			body string
		}
		decided := make(chan outcome, 1)
		edited := make(chan outcome, 1)
		go func() {
			<-start
			rec := httptest.NewRecorder()
			f.app.handleDecideMCPApproval(rec, approvalHandlerRequest(t, http.MethodPost, f.conversationID.String(), approvalID.String(), `{"decision":"approve","always_allow":true}`, f.initiatorID))
			decided <- outcome{rec.Code, rec.Body.String()}
		}()
		go func() {
			<-start
			rec := mcpPermissionsRequest(t, f, connectionID, `{"permissions":[{"tool_name":"create_record","permission":"deny"}]}`)
			edited <- outcome{rec.Code, rec.Body.String()}
		}()
		close(start)
		var decideRes, editRes outcome
		timeout := time.After(20 * time.Second)
		for i := 0; i < 2; i++ {
			select {
			case decideRes = <-decided:
			case editRes = <-edited:
			case <-timeout:
				t.Fatalf("round %d: decision vs permission edit deadlocked", round)
			}
		}
		if decideRes.code >= 500 || editRes.code >= 500 {
			t.Fatalf("round %d: decide=%d edit=%d, want no 500s", round, decideRes.code, editRes.code)
		}
		var approvalStatus, permission string
		if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&approvalStatus); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT permission FROM project_mcp_tool_permissions WHERE connection_id = $1 AND tool_name = 'create_record'), 'ask')`, connectionID).Scan(&permission); err != nil {
			t.Fatal(err)
		}
		// Either the decision won (approved, rule written by the winner)
		// or the deny won (invalidated, rule deny, no resurrection).
		// An approved call with a later deny still never dispatches: the
		// worker rechecks Deny at dispatch.
		if approvalStatus == "invalidated" && permission != "deny" {
			t.Fatalf("round %d: approval=%q permission=%q: revoked call resurrected into %q", round, approvalStatus, permission, permission)
		}
		if approvalStatus != "approved" && approvalStatus != "invalidated" {
			t.Fatalf("round %d: approval=%q, want a terminal decision state", round, approvalStatus)
		}
		// Park the spent turn so the next round can seed fresh: the active
		// turn index allows only one live turn per conversation.
		if _, err := f.pool.Exec(f.ctx, `UPDATE ai_turns SET status = 'completed' WHERE conversation_id = $1`, f.conversationID); err != nil {
			t.Fatal(err)
		}
	}
}
