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

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// newUserQuestionTestPool uses the dedicated disposable database, never DATABASE_URL.
func newUserQuestionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("USER_QUESTION_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("USER_QUESTION_TEST_DATABASE_URL is not set")
	}
	pool, err := internaldb.Connect(context.Background(), databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("user question test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var version int
	if err := pool.QueryRow(context.Background(), `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil || version < 108 {
		t.Skipf("user question schema not migrated (goose %d, want >= 108)", version)
	}
	return pool
}

type userQuestionFixture struct {
	app            *App
	pool           *pgxpool.Pool
	ctx            context.Context
	initiatorID    pgtype.UUID
	memberID       pgtype.UUID
	conversationID pgtype.UUID
}

const userQuestionFixtureArgs = `{"questions": [{"id": "q1", "header": "Scope", "prompt": "What scope?", "mode": "single", "allow_custom": false, "options": [{"id": "a", "label": "Alpha"}, {"id": "b", "label": "Beta"}]}, {"id": "q2", "header": "Pages", "prompt": "Which pages?", "mode": "multiple", "allow_custom": true, "options": [{"id": "x", "label": "Ex"}, {"id": "y", "label": "Why"}]}]}`

func newUserQuestionFixture(t *testing.T) userQuestionFixture {
	t.Helper()
	pool := newUserQuestionTestPool(t)
	ctx := context.Background()
	name := fmt.Sprintf("user-question-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	newUser := func(role string) pgtype.UUID {
		var userID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('user-question-test',$1,$2) RETURNING id`, name+role, name+role+"@example.com").Scan(&userID); err != nil {
			t.Fatalf("create user: %v", err)
		}
		if role != "outsider" {
			if _, err := pool.Exec(ctx, `INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,$3)`, orgID, userID, role); err != nil {
				t.Fatalf("add member: %v", err)
			}
		}
		return userID
	}
	initiatorID := newUser("owner")
	memberID := newUser("member")
	_ = newUser("outsider")
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects(organization_id,name,base_url) VALUES($1,'q','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var conversationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title) VALUES($1,$2,'t') RETURNING id`, projectID, initiatorID).Scan(&conversationID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE auth_provider = 'user-question-test' AND auth_subject LIKE $1`, name+"%")
	})
	app := &App{
		DB:         pool,
		Queries:    sqlc.New(pool),
		Config:     config.Config{GoogleTokenEncryptionSecret: mcpTestEncryptionSecret},
		GSCService: gsc.NewService("", "", "", mcpTestEncryptionSecret, 1<<20),
	}
	return userQuestionFixture{app: app, pool: pool, ctx: ctx, initiatorID: initiatorID, memberID: memberID, conversationID: conversationID}
}

func seedWaitingQuestion(t *testing.T, f userQuestionFixture) (pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	var conversationID pgtype.UUID
	var projectID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT project_id FROM ai_conversations WHERE id = $1`, f.conversationID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title) VALUES($1,$2,'t') RETURNING id`, projectID, f.initiatorID).Scan(&conversationID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	var turnID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,output_started_at) VALUES($1,$2,'waiting_for_user','none','none','m','v',$3,decode(repeat('00',32),'hex'),now()) RETURNING id`,
		conversationID, f.initiatorID, fmt.Sprintf("seed-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','partial','working')`, turnID); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	questions, err := aichattools.ValidateAskUserQuestionArgs(json.RawMessage(userQuestionFixtureArgs))
	if err != nil {
		t.Fatal(err)
	}
	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		t.Fatal(err)
	}
	var requestID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO ai_user_question_requests(turn_id,tool_call_id,questions,status) VALUES($1,'call-q',$2,'pending') RETURNING id`, turnID, string(questionsJSON)).Scan(&requestID); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-q','ask_user_question',$2,'awaiting')`, turnID, userQuestionFixtureArgs); err != nil {
		t.Fatalf("seed tool call: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_user_question_checkpoints(turn_id,question_request_id,messages,usage,budgets,rundown,deferred_calls,round,next_seq) VALUES($1,$2,'[]','{}','{}','{}','[]',0,1)`, turnID, requestID); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	return conversationID, turnID, requestID
}

func userQuestionHandlerRequest(t *testing.T, method, conversationID, requestID, body string, userID pgtype.UUID) *http.Request {
	t.Helper()
	target := "/ai/conversations/" + conversationID + "/questions"
	if requestID != "" {
		target += "/" + requestID + "/answer"
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("conversationID", conversationID)
	if requestID != "" {
		routeContext.URLParams.Add("questionRequestID", requestID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

func decodeQuestionBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode question response: %v (%s)", err, rec.Body.String())
	}
	return body
}

const userQuestionAnswerBody = `{"decision": "answer", "answers": [{"question_id": "q2", "selected_option_ids": ["y", "x"], "custom_text": "plus this"}, {"question_id": "q1", "selected_option_ids": ["b"]}]}`

func TestUserQuestionAnswerQueuesSameTurn(t *testing.T) {
	f := newUserQuestionFixture(t)
	conversationID, turnID, requestID := seedWaitingQuestion(t, f)

	rec := httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conversationID.String(), requestID.String(), userQuestionAnswerBody, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("answer: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeQuestionBody(t, rec)
	request, _ := body["question_request"].(map[string]any)
	if request["status"] != "answered" || body["turn_id"] != turnID.String() {
		t.Fatalf("answer body = %v", body)
	}

	answers, _ := request["answers"].([]any)
	if len(answers) != 2 || answers[0].(map[string]any)["question_id"] != "q1" {
		t.Fatalf("answers not canonical: %v", answers)
	}
	first, _ := answers[0].(map[string]any)["selected_option_ids"].([]any)
	if len(first) != 1 || first[0] != "b" {
		t.Fatalf("q1 selections = %v", first)
	}
	second, _ := answers[1].(map[string]any)["selected_option_ids"].([]any)
	if len(second) != 2 || second[0] != "x" || second[1] != "y" {
		t.Fatalf("q2 selections not in option order: %v", second)
	}
	var turnStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "queued" {
		t.Fatalf("turn status = %q, want the SAME turn requeued", turnStatus)
	}
	var resolved int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'question_resolved' AND payload ? 'question_request'`, turnID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatal("question_resolved event with wrapped payload not persisted atomically")
	}

	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conversationID.String(), requestID.String(), userQuestionAnswerBody, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat answer: %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conversationID.String(), requestID.String(), `{"decision": "skip"}`, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting answer: %d, want 409", rec.Code)
	}
}

func TestUserQuestionAnswerDirectReplyAndSkip(t *testing.T) {
	f := newUserQuestionFixture(t)
	replyConversation, _, requestID := seedWaitingQuestion(t, f)

	rec := httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, replyConversation.String(), requestID.String(), `{"decision": "answer", "reply": "just do the cheap one"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("direct reply: %d (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeQuestionBody(t, rec); body["question_request"].(map[string]any)["reply"] != "just do the cheap one" {
		t.Fatalf("reply body = %v", body)
	}

	skipConversation, _, skipID := seedWaitingQuestion(t, f)
	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, skipConversation.String(), skipID.String(), `{"decision": "skip"}`, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("skip: %d (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeQuestionBody(t, rec); body["question_request"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("skip body = %v", body)
	}
}

func TestUserQuestionAnswerGuards(t *testing.T) {
	f := newUserQuestionFixture(t)
	conversationID, turnID, requestID := seedWaitingQuestion(t, f)
	conv, req := conversationID.String(), requestID.String()

	rec := httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conv, req, userQuestionAnswerBody, f.memberID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member answer: %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conv, req, `{"decision": "answer", "answers": [], "bogus": 1}`, f.initiatorID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conv, req, `{"decision": "answer", "answers": [{"question_id": "q1", "selected_option_ids": ["b"]}]}`, f.initiatorID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("partial answers: %d, want 400", rec.Code)
	}
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_user_question_requests WHERE id = $1`, requestID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("rejected answer mutated request to %q", status)
	}

	if _, err := f.pool.Exec(f.ctx, `UPDATE ai_turns SET status = 'completed' WHERE id = $1`, turnID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conv, req, userQuestionAnswerBody, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale answer: %d, want 409", rec.Code)
	}
}

func TestUserQuestionListAndHistory(t *testing.T) {
	f := newUserQuestionFixture(t)
	conversationID, turnID, requestID := seedWaitingQuestion(t, f)

	rec := httptest.NewRecorder()
	f.app.handleListUserQuestionRequests(rec, userQuestionHandlerRequest(t, http.MethodGet, conversationID.String(), "", "", f.memberID))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rec.Code, rec.Body.String())
	}
	var listed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	requests, _ := listed["question_requests"].([]any)
	if len(requests) != 1 || requests[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("listed = %v", listed)
	}

	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conversationID.String(), requestID.String(), userQuestionAnswerBody, f.initiatorID))
	if rec.Code != http.StatusOK {
		t.Fatalf("answer: %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE ai_turns SET status = 'completed' WHERE id = $1`, turnID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	f.app.handleListUserQuestionRequests(rec, userQuestionHandlerRequest(t, http.MethodGet, conversationID.String(), "", "", f.memberID))
	if rec.Code != http.StatusOK {
		t.Fatalf("list after resolve: %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	requests, _ = listed["question_requests"].([]any)
	if len(requests) != 1 || requests[0].(map[string]any)["status"] != "answered" {
		t.Fatalf("history lost the resolved card: %v", listed)
	}
	if _, ok := requests[0].(map[string]any)["answers"]; !ok {
		t.Fatalf("resolved card missing answers: %v", requests[0])
	}

	turnReq := httptest.NewRequest(http.MethodGet, "/ai/turns/"+turnID.String(), nil)
	turnRoute := chi.NewRouteContext()
	turnRoute.URLParams.Add("turnID", turnID.String())
	turnCtx := context.WithValue(turnReq.Context(), chi.RouteCtxKey, turnRoute)
	turnCtx = withPrincipal(turnCtx, Principal{User: sqlc.User{ID: f.initiatorID}})
	rec = httptest.NewRecorder()
	f.app.handleGetAITurn(rec, turnReq.WithContext(turnCtx))
	if rec.Code != http.StatusOK {
		t.Fatalf("turn snapshot: %d (%s)", rec.Code, rec.Body.String())
	}
	var snapshot map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	cards, _ := snapshot["question_requests"].([]any)
	if len(cards) != 1 || cards[0].(map[string]any)["status"] != "answered" {
		t.Fatalf("turn snapshot lost the resolved card: %v", snapshot["question_requests"])
	}
}

func TestUserQuestionCancelStopsAtomically(t *testing.T) {
	f := newUserQuestionFixture(t)
	conversationID, turnID, requestID := seedWaitingQuestion(t, f)
	_ = conversationID

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ai/turns/"+turnID.String()+"/cancel", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("turnID", turnID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
	f.app.handleCancelAITurn(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d (%s)", rec.Code, rec.Body.String())
	}
	var requestStatus, turnStatus string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_user_question_requests WHERE id = $1`, requestID).Scan(&requestStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if requestStatus != "cancelled" || turnStatus != "stopped" {
		t.Fatalf("request=%q turn=%q, want cancelled/stopped", requestStatus, turnStatus)
	}
	var checkpoint, resolved int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM ai_user_question_checkpoints WHERE turn_id = $1`, turnID).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'question_resolved' AND payload ? 'question_request'`, turnID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if checkpoint != 0 || resolved != 1 {
		t.Fatalf("checkpoint=%d resolved=%d, want 0/1: stop must drop the checkpoint with a wrapped event", checkpoint, resolved)
	}

	rec = httptest.NewRecorder()
	f.app.handleAnswerUserQuestionRequest(rec, userQuestionHandlerRequest(t, http.MethodPost, conversationID.String(), requestID.String(), userQuestionAnswerBody, f.initiatorID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("answer after cancel: %d, want 409", rec.Code)
	}
}

func TestUserQuestionCancelAfterAnswerDropsCheckpoint(t *testing.T) {
	for _, status := range []string{"queued", "running"} {
		t.Run(status, func(t *testing.T) {
			f := newUserQuestionFixture(t)
			conversationID, turnID, requestID := seedWaitingQuestion(t, f)
			answer := httptest.NewRecorder()
			f.app.handleAnswerUserQuestionRequest(answer, userQuestionHandlerRequest(t, http.MethodPost, conversationID.String(), requestID.String(), userQuestionAnswerBody, f.initiatorID))
			if answer.Code != http.StatusOK {
				t.Fatalf("answer: %d (%s)", answer.Code, answer.Body.String())
			}
			if status == "running" {
				if _, err := f.pool.Exec(f.ctx, `UPDATE ai_turns SET status = 'running', claimed_by = 'question-cancel-test', lease_expires_at = now() + interval '1 minute' WHERE id = $1`, turnID); err != nil {
					t.Fatal(err)
				}
			}
			request := httptest.NewRequest(http.MethodPost, "/ai/turns/"+turnID.String()+"/cancel", nil)
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("turnID", turnID.String())
			ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
			ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: f.initiatorID}})
			cancel := httptest.NewRecorder()
			f.app.handleCancelAITurn(cancel, request.WithContext(ctx))
			if cancel.Code != http.StatusOK {
				t.Fatalf("cancel: %d (%s)", cancel.Code, cancel.Body.String())
			}
			var checkpointCount int
			var requestStatus, turnStatus string
			var cancelRequested bool
			if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM ai_user_question_checkpoints WHERE turn_id = $1), q.status, t.status, t.cancel_requested_at IS NOT NULL FROM ai_user_question_requests AS q JOIN ai_turns AS t ON t.id = q.turn_id WHERE q.id = $2`, turnID, requestID).Scan(&checkpointCount, &requestStatus, &turnStatus, &cancelRequested); err != nil {
				t.Fatal(err)
			}
			if checkpointCount != 0 || requestStatus != "answered" || !cancelRequested {
				t.Fatalf("checkpoint=%d request=%q cancelled=%v, want 0/answered/true", checkpointCount, requestStatus, cancelRequested)
			}
			expectedTurnStatus := "stopped"
			if status == "running" {
				expectedTurnStatus = "running"
			}
			if turnStatus != expectedTurnStatus {
				t.Fatalf("turn=%q, want %q", turnStatus, expectedTurnStatus)
			}
		})
	}
}
