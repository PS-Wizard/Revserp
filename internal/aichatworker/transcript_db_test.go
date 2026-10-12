package aichatworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func workerTranscriptPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("USER_QUESTION_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("USER_QUESTION_TEST_DATABASE_URL is not set")
	}
	pool, err := internaldb.Connect(context.Background(), databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("transcript test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var version int
	if err := pool.QueryRow(context.Background(), `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil || version < 109 {
		t.Skipf("transcript schema not migrated (goose %d, want >= 109)", version)
	}
	return pool
}

type workerTranscriptRound struct {
	text      string
	calls     []ai.ToolCall
	reasoning string
	err       error
}

type workerTranscriptProvider struct {
	mu       sync.Mutex
	rounds   []workerTranscriptRound
	requests []ai.Request
}

func (p *workerTranscriptProvider) Stream(_ context.Context, request ai.Request, emit func(ai.Event) error) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	var script workerTranscriptRound
	if len(p.rounds) > 0 {
		script = p.rounds[0]
		p.rounds = p.rounds[1:]
	}
	p.mu.Unlock()
	if request.OnReasoningDelta != nil && script.reasoning != "" {
		request.OnReasoningDelta(script.reasoning)
	}
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
	if script.err != nil {
		return script.err
	}
	usage := ai.Usage{Prompt: 10, Reasoning: 2, Completion: 5, Total: 17}
	return emit(ai.Event{Usage: &usage})
}

func (p *workerTranscriptProvider) captured() []ai.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.Request(nil), p.requests...)
}

func seedWorkerConversation(t *testing.T, pool *pgxpool.Pool) (userID, conversationID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("transcript-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE auth_provider = 'transcript-test' AND auth_subject LIKE $1`, name+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE name = $1`, name)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('transcript-test',$1,$2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects(organization_id,name,base_url) VALUES($1,'t','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title) VALUES($1,$2,'t') RETURNING id`, projectID, userID).Scan(&conversationID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return userID, conversationID
}

func seedWorkerTurn(t *testing.T, pool *pgxpool.Pool, conversationID, userID pgtype.UUID, model, userText string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var turnID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,queued_at) VALUES($1,$2,'queued','none','none',$3,'chat-v4',$4,decode(repeat('00',32),'hex'),now()) RETURNING id`,
		conversationID, userID, model, fmt.Sprintf("t-%d", time.Now().UnixNano())).Scan(&turnID); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete',$2),($1,'assistant','pending','')`, turnID, userText); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	return turnID
}

func runWorkerTurn(t *testing.T, pool *pgxpool.Pool, provider ai.Streamer, turnID pgtype.UUID) {
	t.Helper()
	w := New(pool, provider, Config{ID: fmt.Sprintf("transcript-%d", time.Now().UnixNano())})
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != turnID {
		t.Fatalf("claimed %s, want %s", claimed.ID.String(), turnID.String())
	}
	w.run(context.Background(), claimed)
}

func readWorkerAssistant(t *testing.T, pool *pgxpool.Pool, turnID pgtype.UUID) (content string, blocks []byte, status string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT content, COALESCE(content_blocks, '[]'::jsonb), status FROM ai_messages WHERE turn_id = $1 AND role = 'assistant'`, turnID).Scan(&content, &blocks, &status); err != nil {
		t.Fatalf("read assistant message: %v", err)
	}
	return content, blocks, status
}

func countWorkerEvents(t *testing.T, pool *pgxpool.Pool, turnID pgtype.UUID, eventType string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = $2`, turnID, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorkerTranscriptReplayAcrossTurns(t *testing.T) {
	pool := workerTranscriptPool(t)
	userID, conversationID := seedWorkerConversation(t, pool)
	turn1 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "check the page")
	provider1 := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "looking into it", calls: []ai.ToolCall{{ID: "call-1", Name: "read_issues", Args: `{"limit": 5}`}}, reasoning: "first private reasoning"},
		{text: "final answer", reasoning: "final private reasoning"},
	}}
	runWorkerTurn(t, pool, provider1, turn1)

	var transcriptRaw []byte
	if err := pool.QueryRow(context.Background(), `SELECT transcript FROM ai_turn_transcripts WHERE turn_id = $1`, turn1).Scan(&transcriptRaw); err != nil {
		t.Fatalf("transcript row: %v", err)
	}
	var stored []transcriptMessage
	if err := json.Unmarshal(transcriptRaw, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("transcript messages=%d, want assistant+tool+assistant", len(stored))
	}
	if stored[0].ReasoningContent != "first private reasoning" || stored[2].ReasoningContent != "final private reasoning" {
		t.Fatalf("transcript reasoning lost: %+v", stored)
	}

	turn2 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "and now?")
	provider2 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "second answer"}}}
	runWorkerTurn(t, pool, provider2, turn2)

	requests := provider2.captured()
	if len(requests) == 0 {
		t.Fatal("second turn made no provider request")
	}
	replayed := requests[0].Messages
	joined := ""
	for _, message := range replayed {
		joined += "\n[" + string(message.Role) + "]" + message.Content + "|" + message.ReasoningContent
	}
	for _, want := range []string{"first private reasoning", "looking into it", "final private reasoning", "final answer", "and now?"} {
		if !strings.Contains(joined, want) {
			t.Errorf("replayed history missing %q:\n%s", want, joined)
		}
	}
	if count := strings.Count(joined, "check the page"); count != 1 {
		t.Errorf("first user message appears %d times, want exactly once (no duplication)", count)
	}
}

func TestWorkerFalseApologyEvidencePreserved(t *testing.T) {
	pool := workerTranscriptPool(t)
	userID, conversationID := seedWorkerConversation(t, pool)
	turn1 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "fix the site")
	provider1 := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "checking", calls: []ai.ToolCall{{ID: "call-1", Name: "read_issues", Args: `{"limit": 5}`}}, reasoning: "r1"},
		{text: "Sorry, I failed to find anything."},
	}}
	runWorkerTurn(t, pool, provider1, turn1)

	turn2 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "are you sure?")
	provider2 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "rechecked"}}}
	runWorkerTurn(t, pool, provider2, turn2)

	requests := provider2.captured()
	if len(requests) == 0 {
		t.Fatal("no second request")
	}
	joined := ""
	for _, message := range requests[0].Messages {
		joined += "\n" + message.Content
	}
	if !strings.Contains(joined, "Sorry, I failed to find anything.") {
		t.Errorf("false apology must stay verbatim in replay:\n%s", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "issue") {
		t.Errorf("tool evidence must accompany the apology:\n%s", joined)
	}
}

func TestWorkerFailedTurnEvidenceReplayed(t *testing.T) {
	pool := workerTranscriptPool(t)
	userID, conversationID := seedWorkerConversation(t, pool)
	turn1 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "do work")
	provider1 := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "trying", calls: []ai.ToolCall{{ID: "call-1", Name: "no_such_tool_xyz", Args: `{}`}}, reasoning: "r1"},
		{err: &ai.ProviderError{Code: "provider_timeout", Temporary: true}},
	}}
	runWorkerTurn(t, pool, provider1, turn1)

	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id = $1`, turn1).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("turn status=%q, want failed", status)
	}

	turn2 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "continue")
	provider2 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "ok"}}}
	runWorkerTurn(t, pool, provider2, turn2)

	requests := provider2.captured()
	joined := ""
	for _, message := range requests[0].Messages {
		joined += "\n" + message.Content
	}
	if !strings.Contains(joined, "unknown tool") {
		t.Errorf("failed executed-tool evidence must replay:\n%s", joined)
	}
}

func TestWorkerImagesInHistory(t *testing.T) {
	pool := workerTranscriptPool(t)
	userID, conversationID := seedWorkerConversation(t, pool)
	turn1 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "look at this")
	if _, err := pool.Exec(context.Background(), `UPDATE ai_messages SET content_blocks = '[{"type":"image","media_type":"image/png","data":"aGVsbG8="}]'::jsonb WHERE turn_id = $1 AND role = 'user'`, turn1); err != nil {
		t.Fatal(err)
	}
	provider1 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "seen"}}}
	runWorkerTurn(t, pool, provider1, turn1)

	turn2 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "next")
	provider2 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "done"}}}
	runWorkerTurn(t, pool, provider2, turn2)

	requests := provider2.captured()
	foundImage := false
	for _, message := range requests[0].Messages {
		if len(message.Images) > 0 {
			foundImage = true
		}
	}
	if !foundImage {
		t.Fatal("prior-turn user image must replay into the next request")
	}
}

func TestWorkerFullHistoryUnderMillionTokens(t *testing.T) {
	pool := workerTranscriptPool(t)
	userID, conversationID := seedWorkerConversation(t, pool)
	for i := 0; i < 3; i++ {
		turn := seedWorkerTurn(t, pool, conversationID, userID, "deepseek-flash", fmt.Sprintf("question %d", i))
		provider := &workerTranscriptProvider{rounds: []workerTranscriptRound{
			{text: fmt.Sprintf("working %d", i), calls: []ai.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "read_issues", Args: `{"limit": 5}`}}, reasoning: fmt.Sprintf("reasoning %d with domain detail", i)},
			{text: fmt.Sprintf("answer %d", i)},
		}}
		runWorkerTurn(t, pool, provider, turn)
	}
	turn4 := seedWorkerTurn(t, pool, conversationID, userID, "deepseek-flash", "summarize all")
	provider4 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "summary"}}}
	runWorkerTurn(t, pool, provider4, turn4)

	requests := provider4.captured()
	if len(requests) == 0 {
		t.Fatal("no fourth request")
	}
	joined := ""
	for _, message := range requests[0].Messages {
		joined += "\n" + message.Content + "|" + message.ReasoningContent
	}
	for _, want := range []string{"answer 0", "answer 1", "answer 2", "reasoning 1 with domain detail", "summarize all"} {
		if !strings.Contains(joined, want) {
			t.Errorf("full history missing %q", want)
		}
	}
	if tokens := ai.EstimateChatInputTokens(requests[0]); tokens > chatInputBudgetTokens("deepseek-flash") {
		t.Fatalf("history=%d tokens over guard", tokens)
	}
}

func TestWorkerCapacityTrimPersistsLedgerNotice(t *testing.T) {
	pool := workerTranscriptPool(t)
	userID, conversationID := seedWorkerConversation(t, pool)
	huge := strings.Repeat("result data ", 200000)
	turn1 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "old huge question")
	provider1 := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "working", calls: []ai.ToolCall{{ID: "call-old", Name: "read_issues", Args: `{"limit": 5}`}}, reasoning: "r0"},
		{text: "old answer"},
	}}
	runWorkerTurn(t, pool, provider1, turn1)
	turn2 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "middle question")
	provider2 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "middle answer"}}}
	runWorkerTurn(t, pool, provider2, turn2)
	if _, err := pool.Exec(context.Background(), `UPDATE ai_turn_transcripts SET transcript = jsonb_set(transcript, '{1,content}', to_jsonb($2::text)) WHERE turn_id = $1`, turn1, huge); err != nil {
		t.Fatalf("grow transcript result: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE ai_tool_calls SET result_content = $2 WHERE turn_id = $1`, turn1, huge); err != nil {
		t.Fatalf("grow tool result: %v", err)
	}

	turn3 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "new question")
	provider3 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "new answer"}}}
	runWorkerTurn(t, pool, provider3, turn3)

	requests := provider3.captured()
	if len(requests) == 0 {
		t.Fatal("no third request")
	}
	joined := ""
	for _, message := range requests[0].Messages {
		joined += "\n" + message.Content
	}
	if strings.Contains(joined, huge[:100]) {
		t.Fatal("dropped huge exchange must not replay")
	}
	if !strings.Contains(joined, "middle answer") || !strings.Contains(joined, "new question") {
		t.Errorf("newest exchanges must stay whole:\n%.500s", joined)
	}
	system := requests[0].Messages[0].Content
	for _, want := range []string{"Omitted write ledger", "read_issues", "call-old"} {
		if !strings.Contains(system, want) {
			t.Errorf("ledger missing %q:\n%.500s", want, system)
		}
	}
	_, blocks, _ := readWorkerAssistant(t, pool, turn3)
	activity := parseAssistantActivityBlocks(blocks)
	foundNotice := false
	for _, block := range activity {
		if block.Status == "complete" && strings.Contains(block.Text, "earlier turn") {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Errorf("trim notice must persist as public activity: %s", blocks)
	}
}

func TestWorkerProgressVsFinalSplitAndSnapshot(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	turn := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "do it")
	provider := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "working on it", calls: []ai.ToolCall{{ID: "call-1", Name: "read_issues", Args: `{"limit": 5}`}}, reasoning: "secret reasoning"},
		{text: "done", reasoning: "final reasoning"},
	}}
	runWorkerTurn(t, pool, provider, turn)

	content, blocks, _ := readWorkerAssistant(t, pool, turn)
	if content != "done" {
		t.Fatalf("content=%q, want final answer only", content)
	}
	activity := parseAssistantActivityBlocks(blocks)
	if len(activity) != 1 || activity[0].Round != 0 || activity[0].Text != "working on it" || activity[0].Status != "complete" {
		t.Fatalf("activity=%v, want single complete round-0 block", activity)
	}
	var untagged int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'text_delta' AND (payload->>'round') IS NULL`, turn).Scan(&untagged); err != nil {
		t.Fatal(err)
	}
	if untagged != 0 {
		t.Fatalf("untagged text_delta=%d, want all new deltas tagged", untagged)
	}
	type roundEvent struct {
		kind string
		text string
	}
	rows, err := pool.Query(ctx, `SELECT payload->>'kind', payload->>'text' FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'assistant_round' ORDER BY id`, turn)
	if err != nil {
		t.Fatal(err)
	}
	var rounds []roundEvent
	for rows.Next() {
		var event roundEvent
		if err := rows.Scan(&event.kind, &event.text); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rounds = append(rounds, event)
	}
	rows.Close()
	if len(rounds) != 2 || rounds[0].kind != "activity" || rounds[0].text != "working on it" || rounds[1].kind != "answer" || rounds[1].text != "done" {
		t.Fatalf("assistant_round events=%+v, want activity then answer with full texts", rounds)
	}
	if rounds[1].text != content {
		t.Fatalf("answer event=%q diverges from content=%q", rounds[1].text, content)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND payload::text LIKE '%secret reasoning%'`, turn).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("reasoning leaked into public events")
	}
	if strings.Contains(content, "secret") || strings.Contains(string(blocks), "secret") {
		t.Fatal("reasoning leaked into public content/activity")
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var snapContent, snapBlocks string
	var cursor int64
	if err := tx.QueryRow(ctx, `SELECT content, COALESCE(content_blocks, '[]'::jsonb)::text FROM ai_messages WHERE turn_id = $1 AND role = 'assistant'`, turn).Scan(&snapContent, &snapBlocks); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM ai_turn_events WHERE turn_id = $1`, turn).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND id > $2`, turn, cursor).Scan(&after); err != nil {
		t.Fatal(err)
	}
	_ = after
	if snapContent != "done" || !strings.Contains(snapBlocks, "working on it") {
		t.Fatalf("snapshot must carry provisional-plus-final state: %q %s", snapContent, snapBlocks)
	}
}

func TestWorkerPauseResumeTranscriptContinuity(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	ancient := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "ancient history")
	runWorkerTurn(t, pool, &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "ancient answer"}}}, ancient)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "help me choose")
	askArgs := `{"questions": [{"id": "q1", "header": "Scope", "prompt": "What scope?", "mode": "single", "allow_custom": false, "options": [{"id": "a", "label": "Alpha"}, {"id": "b", "label": "Beta"}]}]}`
	parkProvider := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "checking first", calls: []ai.ToolCall{{ID: "call-0", Name: "read_issues", Args: `{"limit": 5}`}}, reasoning: "pre-park reasoning"},
		{text: "let me ask", calls: []ai.ToolCall{
			{ID: "call-q", Name: "ask_user_question", Args: askArgs},
			{ID: "call-sib", Name: "read_page", Args: `{"url": "https://example.com/unterminated`},
		}},
	}}
	runWorkerTurn(t, pool, parkProvider, turnID)

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "waiting_for_user" {
		t.Fatalf("status=%q, want waiting_for_user", status)
	}

	// Purge history while parked: the fresh load is shorter than the checkpoint.
	// The resume must anchor from restored live, never the fresh length.
	if _, err := pool.Exec(ctx, `DELETE FROM ai_turns WHERE id = $1`, ancient); err != nil {
		t.Fatalf("purge history: %v", err)
	}

	var requestID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ai_user_question_requests WHERE turn_id = $1`, turnID).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	questions, err := json.Marshal(mustValidQuestions(t, askArgs))
	if err != nil {
		t.Fatal(err)
	}
	decisionStatus, canonicalAnswers, _, err := aichattools.ValidateUserQuestionDecision(questions, "answer",
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
		t.Fatal(err)
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

	resumeProvider := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "Done, using Beta.", reasoning: "final reasoning"}}}
	runWorkerTurn(t, pool, resumeProvider, turnID)

	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("status=%q, want completed", status)
	}
	var transcriptRaw []byte
	if err := pool.QueryRow(ctx, `SELECT transcript FROM ai_turn_transcripts WHERE turn_id = $1`, turnID).Scan(&transcriptRaw); err != nil {
		t.Fatalf("transcript row: %v", err)
	}
	transcriptText := string(transcriptRaw)
	for _, want := range []string{"pre-park reasoning", "call-0", "call-q", "Beta", "final reasoning"} {
		if !strings.Contains(transcriptText, want) {
			t.Errorf("transcript missing %q: %s", want, transcriptText)
		}
	}
	if strings.Count(transcriptText, "ancient history") != 0 {
		t.Errorf("restored history must not duplicate into turn-local transcript: %s", transcriptText)
	}
	if strings.Count(transcriptText, "help me choose") != 0 {
		t.Errorf("history user text must not duplicate into transcript: %s", transcriptText)
	}

	_, blocks, _ := readWorkerAssistant(t, pool, turnID)
	activity := parseAssistantActivityBlocks(blocks)
	byRound := map[int]assistantActivityBlock{}
	for _, block := range activity {
		if _, dup := byRound[block.Round]; dup {
			t.Fatalf("duplicate public round %d: %+v", block.Round, activity)
		}
		byRound[block.Round] = block
	}
	parked, ok := byRound[1]
	if !ok || parked.Status != "complete" || !strings.Contains(parked.Text, "let me ask") {
		t.Errorf("finished ask round must close complete before parking: %+v", activity)
	}
	if done, ok := byRound[2]; ok {
		t.Errorf("answer round must not leave an activity block: %+v", done)
	}
}

func TestWorkerRetryRehydratesTranscript(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "retry question")

	seeded, err := json.Marshal(snapshotTranscriptMessages([]ai.Message{
		{Role: ai.RoleAssistant, Content: "retry working", ReasoningContent: "retry reasoning", ToolCalls: []ai.ToolCall{
			{ID: "call-1", Name: "read_issues", Args: `{"limit": 5}`},
			{ID: "call-2", Name: "read_page", Args: `{}`},
		}},
		{Role: ai.RoleTool, Content: "recorded result", ToolCallID: "call-1", Name: "read_issues"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assistantJSON := string(seeded)
	scope := aichattools.Scope{
		RowBudget:         aichattools.NewBudget(200),
		PageContentBudget: aichattools.NewPageContentBudget(96<<10, 5),
		WebBudget:         aichattools.NewWebBudget(3, 3),
		SuggestBudget:     aichattools.NewSuggestBudget(4, 40),
		SkillsBudget:      aichattools.NewSkillBudget(96 << 10),
	}
	scope.RowBudget.Spend(199)
	budgetsJSON, err := json.Marshal(snapshotUserQuestionBudgets(scope))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_turn_transcripts(turn_id,transcript,budgets) VALUES($1,$2::jsonb,$3::jsonb)`, turnID, assistantJSON, string(budgetsJSON)); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status,result_content,summary) VALUES($1,0,'call-1','read_issues','{}','completed','recorded result','ok')`, turnID); err != nil {
		t.Fatalf("seed tool row: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_turns SET status='running',claimed_by='dead',attempt_count=1,lease_expires_at=now()-interval '1 second',output_started_at=NULL WHERE id=$1`, turnID); err != nil {
		t.Fatal(err)
	}
	w := New(pool, nil, Config{ID: "retry-recover"})
	if err := w.recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id=$1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("status=%q, want requeued retry", status)
	}

	provider := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "retried answer"}}}
	runWorkerTurn(t, pool, provider, turnID)

	requests := provider.captured()
	if len(requests) == 0 {
		t.Fatal("retry made no provider request")
	}
	joined := ""
	seenCurrent := false
	afterCurrent := ""
	for _, message := range requests[0].Messages {
		joined += "\n[" + string(message.Role) + ":" + message.ToolCallID + "]" + message.Content
		if message.Role == ai.RoleUser && strings.Contains(message.Content, "retry question") {
			seenCurrent = true
			continue
		}
		if seenCurrent {
			afterCurrent += "\n" + message.Content
		}
	}
	if !seenCurrent {
		t.Fatalf("current user missing from retry request:\n%s", joined)
	}
	for _, want := range []string{"retry working", "recorded result", "outcome unknown", "call-2"} {
		if !strings.Contains(afterCurrent, want) {
			t.Errorf("retried evidence missing %q after current user:\n%s", want, afterCurrent)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_tool_calls WHERE turn_id=$1`, turnID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("tool rows=%d, want 1: retry must never re-execute old calls", rows)
	}
	var budgetsRaw []byte
	if err := pool.QueryRow(ctx, `SELECT budgets FROM ai_turn_transcripts WHERE turn_id=$1`, turnID).Scan(&budgetsRaw); err != nil {
		t.Fatal(err)
	}
	var restored userQuestionBudgets
	if err := json.Unmarshal(budgetsRaw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Row != 1 {
		t.Fatalf("row budget=%d, want restored remainder 1, not a fresh allowance", restored.Row)
	}
}

type gateWorkerProvider struct {
	mu       sync.Mutex
	calls    int
	requests []ai.Request
	release  chan struct{}
}

func (p *gateWorkerProvider) Stream(_ context.Context, request ai.Request, emit func(ai.Event) error) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		if err := emit(ai.Event{ToolCall: &ai.ToolCall{ID: "call-1", Name: "read_issues", Args: `{"limit": 5}`}}); err != nil {
			return err
		}
		return emit(ai.Event{Usage: &ai.Usage{Prompt: 10, Reasoning: 2, Completion: 5, Total: 17}})
	}
	<-p.release
	if err := emit(ai.Event{ToolCall: &ai.ToolCall{ID: "call-2", Name: "read_page", Args: `{}`}}); err != nil {
		return err
	}
	return emit(ai.Event{Usage: &ai.Usage{Prompt: 10, Reasoning: 2, Completion: 5, Total: 17}})
}

func TestWorkerStopMidMultiToolPreservesEvidence(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "multi work")
	provider := &gateWorkerProvider{release: make(chan struct{})}
	w := New(pool, provider, Config{ID: fmt.Sprintf("stop-%d", time.Now().UnixNano())})
	claimed, err := w.claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(context.Background(), claimed)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var completed int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-1' AND status = 'completed'`, turnID).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first tool call never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_turns SET cancel_requested_at = now() WHERE id = $1`, turnID); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("run did not stop")
	}

	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status, COALESCE(error_code,'') FROM ai_turns WHERE id = $1`, turnID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || code != "cancelled" {
		t.Fatalf("turn=%q/%q, want stopped/cancelled", status, code)
	}
	var transcriptRaw []byte
	if err := pool.QueryRow(ctx, `SELECT transcript FROM ai_turn_transcripts WHERE turn_id = $1`, turnID).Scan(&transcriptRaw); err != nil {
		t.Fatalf("transcript row: %v", err)
	}
	transcriptText := string(transcriptRaw)
	for _, want := range []string{"call-1", "call-2"} {
		if !strings.Contains(transcriptText, want) {
			t.Errorf("transcript missing %q (completed write plus pending call): %s", want, transcriptText)
		}
	}
	var pendingRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-2'`, turnID).Scan(&pendingRows); err != nil {
		t.Fatal(err)
	}
	if pendingRows != 0 {
		t.Fatalf("pending call rows=%d, want 0: stopped calls never dispatch", pendingRows)
	}

	turn2 := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "after stop")
	provider2 := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "ok"}}}
	runWorkerTurn(t, pool, provider2, turn2)
	requests := provider2.captured()
	joined := ""
	pairOK := map[string]bool{}
	var lastAssistantCalls []string
	for _, message := range requests[0].Messages {
		joined += "\n" + message.Content
		if message.Role == ai.RoleAssistant {
			lastAssistantCalls = nil
			for _, call := range message.ToolCalls {
				lastAssistantCalls = append(lastAssistantCalls, call.ID)
			}
		}
		if message.Role == ai.RoleTool {
			for _, id := range lastAssistantCalls {
				if id == message.ToolCallID {
					pairOK[id] = true
				}
			}
			lastAssistantCalls = nil
		}
	}
	if !pairOK["call-1"] || !pairOK["call-2"] {
		t.Errorf("every replayed call needs its result paired: %v\n%s", pairOK, joined)
	}
	if !strings.Contains(joined, "outcome unknown") {
		t.Errorf("pending call must replay as unknown, never retried:\n%s", joined)
	}
}

func TestWorkerTimeoutTranscriptRetained(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "slow work")
	blocking := &timeoutWorkerProvider{}
	w := New(pool, blocking, Config{ID: fmt.Sprintf("timeout-%d", time.Now().UnixNano()), TurnTimeout: 400 * time.Millisecond})
	claimed, err := w.claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != turnID {
		t.Fatalf("claimed %s, want %s", claimed.ID.String(), turnID.String())
	}
	w.run(context.Background(), claimed)

	var status, code, content string
	if err := pool.QueryRow(ctx, `SELECT t.status, COALESCE(t.error_code,''), m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, turnID).Scan(&status, &code, &content); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "provider_timeout" || content != "" {
		t.Fatalf("turn=%q/%q content=%q, want failed timeout with blank content", status, code, content)
	}
	var transcriptRaw []byte
	if err := pool.QueryRow(ctx, `SELECT transcript FROM ai_turn_transcripts WHERE turn_id = $1`, turnID).Scan(&transcriptRaw); err != nil {
		t.Fatalf("transcript row: %v", err)
	}
	for _, want := range []string{"partial work", "timeout reasoning"} {
		if !strings.Contains(string(transcriptRaw), want) {
			t.Errorf("timeout transcript missing %q: %s", want, transcriptRaw)
		}
	}
}

type timeoutWorkerProvider struct{}

func (timeoutWorkerProvider) Stream(ctx context.Context, request ai.Request, emit func(ai.Event) error) error {
	if request.OnReasoningDelta != nil {
		request.OnReasoningDelta("timeout reasoning")
	}
	if err := emit(ai.Event{Text: "partial work"}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func splitGooseBlocks(t *testing.T, path string) (up, down []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	text := string(raw)
	upIdx := strings.Index(text, "-- +goose Up")
	downIdx := strings.Index(text, "-- +goose Down")
	if upIdx < 0 || downIdx < 0 || downIdx < upIdx {
		t.Fatalf("migration missing Up/Down markers: %s", path)
	}
	extract := func(block string) []string {
		begin := strings.Index(block, "-- +goose StatementBegin")
		end := strings.Index(block, "-- +goose StatementEnd")
		if begin < 0 || end < 0 || end < begin {
			t.Fatalf("migration block missing statement markers")
		}
		body := block[begin+len("-- +goose StatementBegin") : end]
		var stmts []string
		for _, stmt := range strings.Split(body, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt != "" {
				stmts = append(stmts, stmt)
			}
		}
		return stmts
	}
	return extract(text[upIdx:downIdx]), extract(text[downIdx:])
}

func TestWorkerTranscriptMigrationRollbackWithData(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "rollback probe")
	runWorkerTurn(t, pool, &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "answer", calls: []ai.ToolCall{{ID: "call-1", Name: "read_issues", Args: `{"limit": 5}`}}}}}, turnID)

	up, down := splitGooseBlocks(t, "../../migrations/000109_ai_turn_transcripts.sql")
	if len(up) == 0 || len(down) == 0 {
		t.Fatal("migration must carry Up and Down statements")
	}
	var assistantRounds, textDeltas int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'assistant_round'`, turnID).Scan(&assistantRounds); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'text_delta'`, turnID).Scan(&textDeltas); err != nil {
		t.Fatal(err)
	}
	if assistantRounds == 0 {
		t.Fatal("fixture must include an assistant_round event")
	}
	for _, stmt := range down {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("down statement failed %q: %v", stmt, err)
		}
	}
	var tableGone bool
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_transcripts`).Scan(&tableGone); err == nil {
		t.Fatal("transcript table must be gone after down")
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'assistant_round'`, turnID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("assistant_round events=%d, want only that kind removed", remaining)
	}
	var kept int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'text_delta'`, turnID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != textDeltas {
		t.Fatalf("text_delta events=%d, want preserved %d", kept, textDeltas)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_turn_events(turn_id,event_type,payload) VALUES($1,'assistant_round','{"round":0,"kind":"answer","text":"x"}')`, turnID); err == nil {
		t.Fatal("assistant_round must violate the restored check after down")
	}
	for _, stmt := range up {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("up statement failed %q: %v", stmt, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_turn_events(turn_id,event_type,payload) VALUES($1,'assistant_round','{"round":0,"kind":"answer","text":"x"}')`, turnID); err != nil {
		t.Fatalf("assistant_round must insert after re-up: %v", err)
	}
	var budgets []byte
	if err := pool.QueryRow(ctx, `SELECT budgets FROM ai_turn_transcripts WHERE turn_id = $1`, turnID).Scan(&budgets); err == nil {
		t.Fatal("re-up must start from a clean table without the old row")
	}
}
