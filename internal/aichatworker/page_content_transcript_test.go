package aichatworker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func seedPageContentCrawl(t *testing.T, pool *pgxpool.Pool, conversationID pgtype.UUID, pageURL, phrase string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT project_id FROM ai_conversations WHERE id=$1`, conversationID).Scan(&projectID); err != nil {
		t.Fatalf("conversation project: %v", err)
	}
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls(project_id,status,completed_at) VALUES($1,'completed',now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	blocks := `[{"tag":"p","text":"` + phrase + `","html":"<p>` + phrase + `</p>"}]`
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages(crawl_id,url,title,status_code,content_blocks) VALUES($1,$2,'t',200,$3::jsonb)`, crawlID, pageURL, blocks); err != nil {
		t.Fatalf("create page: %v", err)
	}
	return crawlID
}

func setTurnCrawl(t *testing.T, pool *pgxpool.Pool, turnID, crawlID pgtype.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE ai_turns SET crawl_id=$2 WHERE id=$1`, turnID, crawlID); err != nil {
		t.Fatalf("set turn crawl: %v", err)
	}
}

func assertNoPageKeyNUL(t *testing.T, raw []byte, where string) userQuestionBudgets {
	t.Helper()
	if strings.Contains(string(raw), "\x00") || strings.Contains(string(raw), `\u0000`) {
		t.Fatalf("%s saved a page key NUL: %s", where, raw)
	}
	var decoded userQuestionBudgets
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s budgets decode: %v", where, err)
	}
	for _, key := range decoded.PageContent.SeenKeys {
		if strings.Contains(key, "\x00") {
			t.Fatalf("%s page key has NUL: %q", where, key)
		}
	}
	return decoded
}

func TestWorkerReadPageContentTranscriptRoundTrip(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	const pageURL = "https://example.com/page-content-proof"
	const phrase = "PageEvidenceAlphaBeta"
	crawlID := seedPageContentCrawl(t, pool, conversationID, pageURL, phrase)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "read the stored page")
	setTurnCrawl(t, pool, turnID, crawlID)
	provider := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "checking the page", calls: []ai.ToolCall{{ID: "call-1", Name: "read_page", Args: `{"url":"` + pageURL + `","mode":"content"}`}}, reasoning: "private page reasoning"},
		{text: "done", reasoning: "final private reasoning"},
	}}
	runWorkerTurn(t, pool, provider, turnID)

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id=$1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("status=%q, want completed", status)
	}
	var budgetsRaw []byte
	if err := pool.QueryRow(ctx, `SELECT budgets FROM ai_turn_transcripts WHERE turn_id=$1`, turnID).Scan(&budgetsRaw); err != nil {
		t.Fatalf("transcript budgets row: %v", err)
	}
	decoded := assertNoPageKeyNUL(t, budgetsRaw, "transcript budgets")
	if len(decoded.PageContent.SeenKeys) != 1 {
		t.Fatalf("SeenKeys=%v, want the one read page", decoded.PageContent.SeenKeys)
	}
	var result string
	if err := pool.QueryRow(ctx, `SELECT result_content FROM ai_tool_calls WHERE turn_id=$1 AND call_id='call-1'`, turnID).Scan(&result); err != nil {
		t.Fatalf("tool result: %v", err)
	}
	if !strings.Contains(result, phrase) {
		t.Fatalf("tool content lost evidence %q: %.500s", phrase, result)
	}
	requests := provider.captured()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d, want 2", len(requests))
	}
	joined := ""
	for _, message := range requests[1].Messages {
		joined += "\n" + message.Content
	}
	if !strings.Contains(joined, phrase) {
		t.Errorf("replayed request lost exact tool content:\n%.1000s", joined)
	}
	content, blocks, _ := readWorkerAssistant(t, pool, turnID)
	if content != "done" {
		t.Fatalf("content=%q, want final answer only", content)
	}
	if strings.Contains(content, "private page reasoning") || strings.Contains(string(blocks), "private page reasoning") {
		t.Fatalf("reasoning leaked into public content: %q %s", content, blocks)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id=$1 AND payload::text LIKE '%private page reasoning%'`, turnID).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("reasoning leaked into public events")
	}
	var transcriptRaw []byte
	if err := pool.QueryRow(ctx, `SELECT transcript FROM ai_turn_transcripts WHERE turn_id=$1`, turnID).Scan(&transcriptRaw); err != nil {
		t.Fatalf("transcript row: %v", err)
	}
	for _, want := range []string{"private page reasoning", "final private reasoning", phrase} {
		if !strings.Contains(string(transcriptRaw), want) {
			t.Errorf("private transcript missing %q: %.1000s", want, transcriptRaw)
		}
	}
}

func TestWorkerReadPageBudgetSurvivesUserQuestionPark(t *testing.T) {
	pool := workerTranscriptPool(t)
	ctx := context.Background()
	userID, conversationID := seedWorkerConversation(t, pool)
	const pageURL = "https://example.com/park-proof"
	const phrase = "ParkEvidenceAlphaBeta"
	crawlID := seedPageContentCrawl(t, pool, conversationID, pageURL, phrase)
	turnID := seedWorkerTurn(t, pool, conversationID, userID, "test-model", "read then choose")
	setTurnCrawl(t, pool, turnID, crawlID)
	parkProvider := &workerTranscriptProvider{rounds: []workerTranscriptRound{
		{text: "reading first", calls: []ai.ToolCall{{ID: "call-1", Name: "read_page", Args: `{"url":"` + pageURL + `","mode":"content"}`}}, reasoning: "pre-park reasoning"},
		{text: "let me ask", calls: []ai.ToolCall{{ID: "call-q", Name: "ask_user_question", Args: userQuestionTestArgs}}},
	}}
	runWorkerTurn(t, pool, parkProvider, turnID)

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id=$1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "waiting_for_user" {
		t.Fatalf("status=%q, want waiting_for_user", status)
	}
	var checkpointRaw []byte
	if err := pool.QueryRow(ctx, `SELECT budgets FROM ai_user_question_checkpoints WHERE turn_id=$1`, turnID).Scan(&checkpointRaw); err != nil {
		t.Fatalf("checkpoint budgets row: %v", err)
	}
	parked := assertNoPageKeyNUL(t, checkpointRaw, "checkpoint budgets")
	if len(parked.PageContent.SeenKeys) != 1 {
		t.Fatalf("parked SeenKeys=%v, want the read page", parked.PageContent.SeenKeys)
	}

	var requestID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ai_user_question_requests WHERE turn_id=$1`, turnID).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	questions, err := json.Marshal(mustValidQuestions(t, userQuestionTestArgs))
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

	resumeProvider := &workerTranscriptProvider{rounds: []workerTranscriptRound{{text: "Done, using Beta."}}}
	runWorkerTurn(t, pool, resumeProvider, turnID)

	if err := pool.QueryRow(ctx, `SELECT status FROM ai_turns WHERE id=$1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("status=%q, want completed", status)
	}
	var budgetsRaw []byte
	if err := pool.QueryRow(ctx, `SELECT budgets FROM ai_turn_transcripts WHERE turn_id=$1`, turnID).Scan(&budgetsRaw); err != nil {
		t.Fatalf("transcript budgets row: %v", err)
	}
	final := assertNoPageKeyNUL(t, budgetsRaw, "resumed budgets")
	if len(final.PageContent.SeenKeys) != 1 || final.PageContent.SeenKeys[0] != parked.PageContent.SeenKeys[0] {
		t.Fatalf("SeenKeys=%v, want conserved %v", final.PageContent.SeenKeys, parked.PageContent.SeenKeys)
	}
	if final.PageContent.BytesLeft != parked.PageContent.BytesLeft {
		t.Fatalf("page bytes=%d, want conserved %d", final.PageContent.BytesLeft, parked.PageContent.BytesLeft)
	}
	var checkpoints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_user_question_checkpoints WHERE turn_id=$1`, turnID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 0 {
		t.Fatal("checkpoint not deleted on resume")
	}
	var transcriptRaw []byte
	if err := pool.QueryRow(ctx, `SELECT transcript FROM ai_turn_transcripts WHERE turn_id=$1`, turnID).Scan(&transcriptRaw); err != nil {
		t.Fatalf("transcript row: %v", err)
	}
	for _, want := range []string{"pre-park reasoning", phrase, "Beta"} {
		if !strings.Contains(string(transcriptRaw), want) {
			t.Errorf("resumed transcript missing %q: %.1000s", want, transcriptRaw)
		}
	}
}
