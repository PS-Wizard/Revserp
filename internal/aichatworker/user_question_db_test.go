package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// userQuestionTestPool uses the dedicated disposable database, never DATABASE_URL.
func userQuestionTestPool(t *testing.T) *pgxpool.Pool {
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

type userQuestionScript struct {
	calls []ai.ToolCall
	text  string
}

type userQuestionFakeStreamer struct {
	scripts  []userQuestionScript
	calls    int
	requests []ai.Request
}

func (f *userQuestionFakeStreamer) Stream(_ context.Context, request ai.Request, emit func(ai.Event) error) error {
	f.requests = append(f.requests, request)
	var script userQuestionScript
	if f.calls < len(f.scripts) {
		script = f.scripts[f.calls]
	}
	f.calls++
	for _, call := range script.calls {
		call := call
		if err := emit(ai.Event{ToolCall: &call}); err != nil {
			return err
		}
	}
	if script.text != "" {
		if err := emit(ai.Event{Text: script.text}); err != nil {
			return err
		}
	}
	usage := ai.Usage{Prompt: 10, Reasoning: 2, Completion: 5, Total: 17}
	return emit(ai.Event{Usage: &usage})
}

func seedUserQuestionTurn(t *testing.T, pool *pgxpool.Pool) (userID, conversationID, turnID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("user-question-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE auth_provider = 'user-question-test' AND auth_subject LIKE $1`, name+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE name = $1`, name)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('user-question-test',$1,$2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects(organization_id,name,base_url) VALUES($1,'q','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title) VALUES($1,$2,'t') RETURNING id`, projectID, userID).Scan(&conversationID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,queued_at) VALUES($1,$2,'queued','none','none','test-model','chat-v4',$3,decode(repeat('00',32),'hex'),now()) RETURNING id`, conversationID, userID, fmt.Sprintf("q-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','help me choose'),($1,'assistant','pending','')`, turnID); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	return userID, conversationID, turnID
}

const userQuestionTestArgs = `{"questions": [{"id": "q1", "header": "Scope", "prompt": "What scope?", "mode": "single", "allow_custom": false, "options": [{"id": "a", "label": "Alpha"}, {"id": "b", "label": "Beta", "recommended": true}]}]}`

func TestUserQuestionParkResumeAfterRestart(t *testing.T) {
	pool := userQuestionTestPool(t)
	ctx := context.Background()
	_, _, turnID := seedUserQuestionTurn(t, pool)

	parkProvider := &userQuestionFakeStreamer{scripts: []userQuestionScript{
		{calls: []ai.ToolCall{
			{ID: "call-q", Name: aichattools.AskUserQuestionName, Args: userQuestionTestArgs},
			{ID: "call-sib", Name: "read_page", Args: `{"url": "https://example.com/…truncated`},
		}},
	}}
	w1 := New(pool, parkProvider, Config{ID: "uq-park"})
	claimed, err := w1.claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != turnID {
		t.Fatalf("claimed %s, want %s", claimed.ID.String(), turnID.String())
	}
	w1.run(ctx, claimed)

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "waiting_for_user" {
		t.Fatalf("turn status = %q, want waiting_for_user: the park must release the lease", status)
	}
	var claimedBy *string
	if err := pool.QueryRow(ctx, `SELECT claimed_by FROM ai_turns WHERE id = $1`, turnID).Scan(&claimedBy); err != nil {
		t.Fatal(err)
	}
	if claimedBy != nil {
		t.Fatalf("claimed_by = %q, want NULL: waiting releases the worker", *claimedBy)
	}
	var requestStatus, toolCallID string
	var requestID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id, tool_call_id, status FROM ai_user_question_requests WHERE turn_id = $1`, turnID).Scan(&requestID, &toolCallID, &requestStatus); err != nil {
		t.Fatalf("question request: %v", err)
	}
	if requestStatus != "pending" || toolCallID != "call-q" {
		t.Fatalf("request = %s/%s, want pending/call-q", requestStatus, toolCallID)
	}
	var callStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-q'`, turnID).Scan(&callStatus); err != nil {
		t.Fatalf("question tool call: %v", err)
	}
	if callStatus != "awaiting" {
		t.Fatalf("question call status = %q, want awaiting", callStatus)
	}
	var siblingCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-sib'`, turnID).Scan(&siblingCount); err != nil {
		t.Fatal(err)
	}
	if siblingCount != 0 {
		t.Fatal("deferred sibling ran while waiting for the user")
	}
	var requiredCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'question_required'`, turnID).Scan(&requiredCount); err != nil {
		t.Fatal(err)
	}
	if requiredCount != 1 {
		t.Fatalf("question_required events = %d, want 1", requiredCount)
	}
	var wrapped string
	if err := pool.QueryRow(ctx, `SELECT payload->'question_request'->>'tool_call_id' FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'question_required'`, turnID).Scan(&wrapped); err != nil {
		t.Fatalf("question_required payload: %v", err)
	}
	if wrapped != "call-q" {
		t.Fatalf("question_required payload not wrapped as {question_request}: %q", wrapped)
	}

	questions, err := aichattools.ValidateAskUserQuestionArgs(json.RawMessage(userQuestionTestArgs))
	if err != nil {
		t.Fatal(err)
	}
	_ = questions
	stored, err := json.Marshal(mustValidQuestions(t, userQuestionTestArgs))
	if err != nil {
		t.Fatal(err)
	}
	decisionStatus, canonicalAnswers, _, err := aichattools.ValidateUserQuestionDecision(stored, "answer",
		[]aichattools.UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"b"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(pool)
	decided, err := queries.DecideUserQuestionRequest(ctx, sqlc.DecideUserQuestionRequestParams{
		ID: requestID, Status: decisionStatus, Answers: canonicalAnswers, Reply: "",
		DecidedBy: mustTurnUser(t, pool, ctx, turnID),
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if queued, err := queries.QueueWaitingTurnForQuestions(ctx, decided.TurnID); err != nil || queued != 1 {
		t.Fatalf("queue: %v rows=%d", err, queued)
	}
	resolvedEntity, err := aichattools.MarshalUserQuestionRequest(decided.ID.String(), decided.TurnID.String(), decided.ToolCallID, decided.Status, decided.Questions, decided.Answers, decided.Reply, decided.CreatedAt.Time, ptrTime(decided.AnsweredAt.Time))
	if err != nil {
		t.Fatal(err)
	}
	wrappedResolved, err := json.Marshal(map[string]json.RawMessage{"question_request": resolvedEntity})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.InsertAITurnEvent(ctx, sqlc.InsertAITurnEventParams{TurnID: turnID, EventType: "question_resolved", Payload: wrappedResolved}); err != nil {
		t.Fatal(err)
	}

	resumeProvider := &userQuestionFakeStreamer{scripts: []userQuestionScript{
		{text: "Done, using Beta."},
	}}
	w2 := New(pool, resumeProvider, Config{ID: "uq-resume"})
	claimed2, err := w2.claim(ctx)
	if err != nil {
		t.Fatalf("reclaim after answer: %v (the same turn must requeue)", err)
	}
	if claimed2.ID != turnID {
		t.Fatalf("reclaimed %s, want same turn %s", claimed2.ID.String(), turnID.String())
	}
	w2.run(ctx, claimed2)

	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("turn status = %q, want completed", status)
	}
	var resultContent, resultStatus string
	if err := pool.QueryRow(ctx, `SELECT result_content, status FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-q'`, turnID).Scan(&resultContent, &resultStatus); err != nil {
		t.Fatalf("question result: %v", err)
	}
	if resultStatus != "completed" {
		t.Fatalf("question call status = %q, want completed", resultStatus)
	}

	for _, want := range []string{"Beta (b)", "Scope (q1)"} {
		if !strings.Contains(resultContent, want) {
			t.Errorf("question result missing %q: %q", want, resultContent)
		}
	}
	var questionRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-q'`, turnID).Scan(&questionRows); err != nil {
		t.Fatal(err)
	}
	if questionRows != 1 {
		t.Fatalf("question call rows = %d, want exactly one: resume must not duplicate writes", questionRows)
	}

	var sibStatus, sibArgs, sibSummary string
	if err := pool.QueryRow(ctx, `SELECT status, args::text, summary FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-sib'`, turnID).Scan(&sibStatus, &sibArgs, &sibSummary); err != nil {
		t.Fatalf("deferred sibling: %v", err)
	}
	if sibStatus != "failed" || sibSummary == "" {
		t.Fatalf("sibling = %q/%q, want failed with summary", sibStatus, sibSummary)
	}
	var checkpointCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_user_question_checkpoints WHERE turn_id = $1`, turnID).Scan(&checkpointCount); err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 0 {
		t.Fatal("checkpoint not deleted on resume: a retried claim could replay it")
	}
	if len(resumeProvider.requests) != 1 {
		t.Fatalf("resumed provider requests = %d, want 1: no provider call happens while waiting", len(resumeProvider.requests))
	}
	resumed := resumeProvider.requests[0]
	toolResults := 0
	for _, message := range resumed.Messages {
		if message.Role == ai.RoleTool {
			toolResults++
		}
	}
	if toolResults != 2 {
		t.Fatalf("resumed request has %d tool results, want 2 (answer plus deferred sibling)", toolResults)
	}
}

func mustValidQuestions(t *testing.T, args string) []aichattools.AskUserQuestion {
	t.Helper()
	questions, err := aichattools.ValidateAskUserQuestionArgs(json.RawMessage(args))
	if err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	return questions
}

func mustTurnUser(t *testing.T, pool *pgxpool.Pool, ctx context.Context, turnID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT created_by_user_id FROM ai_turns WHERE id = $1`, turnID).Scan(&userID); err != nil {
		t.Fatalf("turn user: %v", err)
	}
	return userID
}

func ptrTime(t time.Time) *time.Time { return &t }

func seedClaimedQuestionTurn(t *testing.T, pool *pgxpool.Pool, workerID string, mutate string) (claimed turn, requestID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	_, _, turnID := seedUserQuestionTurn(t, pool)
	stored, err := json.Marshal(mustValidQuestions(t, userQuestionTestArgs))
	if err != nil {
		t.Fatal(err)
	}
	_, canonicalAnswers, _, err := aichattools.ValidateUserQuestionDecision(stored, "answer",
		[]aichattools.UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"a"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(pool)
	created, err := queries.CreateUserQuestionRequest(ctx, sqlc.CreateUserQuestionRequestParams{
		TurnID: turnID, ToolCallID: "call-q", Questions: stored,
	})
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}
	requestID = created.ID
	decided, err := queries.DecideUserQuestionRequest(ctx, sqlc.DecideUserQuestionRequestParams{
		ID: requestID, Status: "answered", Answers: canonicalAnswers, Reply: "",
		DecidedBy: mustTurnUser(t, pool, ctx, turnID),
	})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	_ = decided
	if _, err := pool.Exec(ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-q','ask_user_question',$2,'awaiting')`, turnID, userQuestionTestArgs); err != nil {
		t.Fatalf("seed tool call: %v", err)
	}
	if _, err := queries.InsertUserQuestionCheckpoint(ctx, sqlc.InsertUserQuestionCheckpointParams{
		TurnID: turnID, QuestionRequestID: requestID,
		Messages: []byte(`[]`), Usage: []byte(`{}`), Budgets: []byte(`{}`),
		Rundown: []byte(`{}`), DeferredCalls: []byte(`[]`), Round: 0, NextSeq: 1,
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE ai_turns SET status = 'running', claimed_by = $2, attempt_count = 1, started_at = now(), heartbeat_at = now(), lease_expires_at = now() + interval '1 minute', updated_at = now() WHERE id = $1`, turnID, workerID); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if mutate != "" {
		if _, err := pool.Exec(ctx, mutate, turnID); err != nil {
			t.Fatalf("seed mutate: %v", err)
		}
	}
	return turn{ID: turnID}, requestID
}

func assertQuestionRowAwaiting(t *testing.T, pool *pgxpool.Pool, turnID pgtype.UUID) {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-q'`, turnID).Scan(&status); err != nil {
		t.Fatalf("question row: %v", err)
	}
	if status != "awaiting" {
		t.Fatalf("question row = %q, want untouched awaiting", status)
	}
}

func assertCheckpointIntact(t *testing.T, pool *pgxpool.Pool, turnID pgtype.UUID) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_user_question_checkpoints WHERE turn_id = $1`, turnID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("checkpoints = %d, want the intact 1", count)
	}
}

func TestUserQuestionResumeCancelRacedStops(t *testing.T) {
	pool := userQuestionTestPool(t)
	workerID := fmt.Sprintf("uq-cancel-%d", time.Now().UnixNano())
	claimed, _ := seedClaimedQuestionTurn(t, pool, workerID,
		`UPDATE ai_turns SET cancel_requested_at = now() WHERE id = $1`)
	w := New(pool, nil, Config{ID: workerID})

	resumed, err := w.resumeUserQuestionIfParked(context.Background(), claimed, aichattools.NewFilteredRegistry(nil))
	if !errors.Is(err, errUserQuestionStopRaced) {
		t.Fatalf("resume err = %v, want stop-raced sentinel", err)
	}
	if resumed != nil {
		t.Fatal("cancel-raced resume must not return a transcript to send")
	}
	assertQuestionRowAwaiting(t, pool, claimed.ID)
	assertCheckpointIntact(t, pool, claimed.ID)
	var events int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type IN ('tool_result', 'question_resolved')`, claimed.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("cancel-raced resume wrote %d events", events)
	}
}

func TestUserQuestionResumeStaleOwnerWritesNothing(t *testing.T) {
	pool := userQuestionTestPool(t)
	workerID := fmt.Sprintf("uq-stale-%d", time.Now().UnixNano())
	claimed, _ := seedClaimedQuestionTurn(t, pool, workerID,
		`UPDATE ai_turns SET claimed_by = 'other-worker', lease_expires_at = now() - interval '1 minute' WHERE id = $1`)
	w := New(pool, nil, Config{ID: workerID})

	if resumed, err := w.resumeUserQuestionIfParked(context.Background(), claimed, aichattools.NewFilteredRegistry(nil)); err == nil || resumed != nil {
		t.Fatalf("stale resume = %+v, %v; want an error and no transcript", resumed, err)
	}
	assertQuestionRowAwaiting(t, pool, claimed.ID)
	assertCheckpointIntact(t, pool, claimed.ID)
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM ai_user_question_requests WHERE turn_id = $1`, claimed.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "answered" {
		t.Fatalf("request status = %q, want untouched answered", status)
	}
}

func TestUserQuestionResumeMissingRequestCleansUp(t *testing.T) {
	pool := userQuestionTestPool(t)
	ctx := context.Background()
	workerID := fmt.Sprintf("uq-gone-%d", time.Now().UnixNano())
	_, _, turnAID := seedUserQuestionTurn(t, pool)
	stored, err := json.Marshal(mustValidQuestions(t, userQuestionTestArgs))
	if err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(pool)
	created, err := queries.CreateUserQuestionRequest(ctx, sqlc.CreateUserQuestionRequestParams{
		TurnID: turnAID, ToolCallID: "call-other", Questions: stored,
	})
	if err != nil {
		t.Fatalf("seed other request: %v", err)
	}
	_, _, turnBID := seedUserQuestionTurn(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-q','ask_user_question',$2,'awaiting')`, turnBID, userQuestionTestArgs); err != nil {
		t.Fatalf("seed tool call: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_user_question_checkpoints(turn_id,question_request_id,messages,usage,budgets,rundown,deferred_calls,round,next_seq) VALUES($1,$2,'[]','{}','{}','{}','[]',0,1)`, turnBID, created.ID); err != nil {
		t.Fatalf("seed mismatched checkpoint: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_turns SET status = 'running', claimed_by = $2, attempt_count = 1, started_at = now(), heartbeat_at = now(), lease_expires_at = now() + interval '1 minute', updated_at = now() WHERE id = $1`, turnBID, workerID); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	w := New(pool, nil, Config{ID: workerID})

	shortCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	resumed, err := w.resumeUserQuestionIfParked(shortCtx, turn{ID: turnBID}, aichattools.NewFilteredRegistry(nil))
	if err == nil || resumed != nil {
		t.Fatalf("mismatched resume = %+v, %v; want an error and no transcript", resumed, err)
	}
	if elapsed := time.Since(start); elapsed >= 20*time.Second {
		t.Fatal("mismatched resume hung instead of failing fast")
	}
	assertQuestionRowAwaiting(t, pool, turnBID)
	assertCheckpointIntact(t, pool, turnBID)
	var otherStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_user_question_requests WHERE id = $1`, created.ID).Scan(&otherStatus); err != nil {
		t.Fatal(err)
	}
	if otherStatus != "pending" {
		t.Fatalf("other turn request = %q, want untouched pending", otherStatus)
	}
	var turnStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnBID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "running" {
		t.Fatalf("turn status = %q, want untouched running: the caller owns finalization", turnStatus)
	}
}

func TestUserQuestionResumeDisabledToolCancels(t *testing.T) {
	pool := userQuestionTestPool(t)
	ctx := context.Background()
	workerID := fmt.Sprintf("uq-disabled-%d", time.Now().UnixNano())
	claimed, _ := seedClaimedQuestionTurn(t, pool, workerID, "")
	if _, err := pool.Exec(ctx, `INSERT INTO organization_features(org_id, disabled_ai_tools) SELECT p.organization_id, ARRAY['ask_user_question'] FROM ai_turns AS t JOIN ai_conversations AS c ON c.id = t.conversation_id JOIN projects AS p ON p.id = c.project_id WHERE t.id = $1 ON CONFLICT (org_id) DO UPDATE SET disabled_ai_tools = EXCLUDED.disabled_ai_tools`, claimed.ID); err != nil {
		t.Fatalf("disable tool: %v", err)
	}
	w := New(pool, nil, Config{ID: workerID})

	resumed, err := w.resumeUserQuestionIfParked(ctx, claimed, aichattools.NewFilteredRegistry(nil))
	if err == nil || resumed != nil {
		t.Fatalf("disabled resume = %+v, %v; want an error and no transcript", resumed, err)
	}
	var requestStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_user_question_requests WHERE turn_id = $1`, claimed.ID).Scan(&requestStatus); err != nil {
		t.Fatal(err)
	}
	if requestStatus != "answered" {
		t.Fatalf("request status = %q, want the untouched answered decision", requestStatus)
	}
	var checkpoints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_user_question_checkpoints WHERE turn_id = $1`, claimed.ID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 0 {
		t.Fatal("checkpoint not dropped: a retried claim could replay it")
	}
	assertQuestionRowAwaiting(t, pool, claimed.ID)
}
