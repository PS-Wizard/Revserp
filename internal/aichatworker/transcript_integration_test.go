package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aiskills"
)

const transcriptIntegrationSkillMarker = "TRANSCRIPT_SKILL_ROOT_MARKER"

func transcriptIntegrationEnv(t *testing.T) {
	t.Helper()
	dedicated := strings.TrimSpace(os.Getenv("USER_QUESTION_TEST_DATABASE_URL"))
	if dedicated == "" {
		t.Skip("USER_QUESTION_TEST_DATABASE_URL is not set")
	}
	t.Setenv("DATABASE_URL", dedicated)
}

func transcriptIntegrationSkills(t *testing.T) *aiskills.Catalog {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "t", "skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: skill\ndescription: Transcript test skill.\n---\n\n" + transcriptIntegrationSkillMarker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return aiskills.New(filepath.Dir(filepath.Dir(dir)))
}

func queueConversationTurn(t *testing.T, w *Worker, conversation, user pgtype.UUID, text string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := w.pool.QueryRow(context.Background(), `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,queued_at) VALUES($1,$2,'queued','none','none','m','v',$3,decode(repeat('00',32),'hex'),now()-interval '100 years') RETURNING id`, conversation, user, fmt.Sprint(time.Now().UnixNano())).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete',$2),($1,'assistant','pending','')`, id, text); err != nil {
		t.Fatal(err)
	}
	return id
}

func transcriptIntegrationPayloads(t *testing.T, w *Worker, turnID pgtype.UUID) []string {
	t.Helper()
	rows, err := w.pool.Query(context.Background(), `SELECT payload::text FROM ai_turn_events WHERE turn_id = $1`, turnID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return payloads
}

func transcriptIntegrationStored(t *testing.T, w *Worker, turnID pgtype.UUID) []transcriptMessage {
	t.Helper()
	var raw []byte
	if err := w.pool.QueryRow(context.Background(), `SELECT transcript FROM ai_turn_transcripts WHERE turn_id = $1`, turnID).Scan(&raw); err != nil {
		t.Fatalf("private transcript row: %v", err)
	}
	var stored []transcriptMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestTranscriptIntegrationToolRoundPrivacy(t *testing.T) {
	transcriptIntegrationEnv(t)
	w, _, user, project := testWorker(t)
	w.Skills = transcriptIntegrationSkills(t)
	provider := &roundProvider{
		rounds: [][]ai.Event{
			{
				{Text: "Checking..."},
				{ToolCall: &ai.ToolCall{ID: "call-list", Name: "list_skills", Args: `{}`}},
				{ToolCall: &ai.ToolCall{ID: "call-root", Name: "read_skill", Args: `{"skill":"t/skill"}`}},
			},
			{{Text: "final answer"}},
		},
		reasoning: []string{"first private reasoning", "final private reasoning"},
	}
	w.provider = provider
	id := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	var status, content string
	var blocks []byte
	if err := w.pool.QueryRow(context.Background(), `SELECT t.status, m.content, COALESCE(m.content_blocks, '[]'::jsonb) FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content, &blocks); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "final answer" {
		t.Fatalf("turn=%q content=%q, want completed/final answer only", status, content)
	}
	if !strings.Contains(string(blocks), "Checking...") {
		t.Fatalf("public progress missing tool-round text: %s", blocks)
	}
	for _, leaked := range []string{"first private reasoning", "final private reasoning"} {
		if strings.Contains(string(blocks), leaked) {
			t.Fatalf("private reasoning leaked into public blocks: %q", leaked)
		}
	}
	joined := strings.Join(transcriptIntegrationPayloads(t, w, id), "\n")
	if !strings.Contains(joined, "Checking...") || !strings.Contains(joined, "final answer") {
		t.Fatalf("public events missing progress or answer: %s", joined)
	}
	for _, leaked := range []string{"first private reasoning", "final private reasoning"} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("private reasoning leaked into public events: %q", leaked)
		}
	}

	stored := transcriptIntegrationStored(t, w, id)
	if len(stored) != 4 {
		t.Fatalf("private transcript messages=%d, want assistant+2 tools+assistant", len(stored))
	}
	if stored[0].ReasoningContent != "first private reasoning" || stored[0].Content != "Checking..." {
		t.Fatalf("tool-round assistant lost: %+v", stored[0])
	}
	if len(stored[0].ToolCalls) != 2 || stored[0].ToolCalls[0].ID != "call-list" || stored[0].ToolCalls[1].ID != "call-root" {
		t.Fatalf("tool-round calls lost: %+v", stored[0].ToolCalls)
	}
	if stored[1].ToolCallID != "call-list" || !strings.Contains(stored[1].Content, "t/skill") {
		t.Fatalf("list_skills result lost: %+v", stored[1])
	}
	if stored[2].ToolCallID != "call-root" || !strings.Contains(stored[2].Content, transcriptIntegrationSkillMarker) {
		t.Fatalf("read_skill result lost: %+v", stored[2])
	}
	if stored[3].Content != "final answer" || stored[3].ReasoningContent != "final private reasoning" {
		t.Fatalf("final assistant lost: %+v", stored[3])
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider streams=%d, want 2", len(provider.requests))
	}
	replayed := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == ai.RoleAssistant && len(message.ToolCalls) == 2 && message.ReasoningContent == "first private reasoning" {
			replayed = true
		}
	}
	if !replayed {
		t.Fatal("second wire request did not replay the tool round with private reasoning")
	}
}

func TestTranscriptIntegrationUnavailableSearchReplay(t *testing.T) {
	transcriptIntegrationEnv(t)
	w, _, user, project := testWorker(t)
	w.Skills = transcriptIntegrationSkills(t)
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-web", Name: "web_search", Args: `{"query":"x"}`}}},
		{{ToolCall: &ai.ToolCall{ID: "call-root", Name: "read_skill", Args: `{"skill":"t/skill"}`}}},
		{{Text: "turn one final"}},
	}}
	turn1 := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	var conversation pgtype.UUID
	if err := w.pool.QueryRow(context.Background(), `SELECT conversation_id FROM ai_turns WHERE id = $1`, turn1).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	provider2 := &roundProvider{rounds: [][]ai.Event{{{Text: "follow-up done"}}}}
	w.provider = provider2
	turn2 := queueConversationTurn(t, w, conversation, user, "second user question")
	claimed2, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed2.ID != turn2 {
		t.Fatalf("claimed %s, want %s", claimed2.ID.String(), turn2.String())
	}
	w.run(context.Background(), claimed2)

	if len(provider2.requests) != 1 {
		t.Fatalf("second turn streams=%d, want exactly 1 with no repeated tools", len(provider2.requests))
	}
	var toolCalls int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1`, turn2).Scan(&toolCalls); err != nil {
		t.Fatal(err)
	}
	if toolCalls != 0 {
		t.Fatalf("second turn executed %d tools, want none", toolCalls)
	}
	joined := ""
	for _, message := range provider2.requests[0].Messages {
		joined += "\n" + message.Content
	}
	for _, want := range []string{"Web search is not available for this project.", transcriptIntegrationSkillMarker, "turn one final"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("second wire request missing evidence %q:\n%s", want, joined)
		}
	}
}

func TestTranscriptIntegrationStaleOwnerGuard(t *testing.T) {
	transcriptIntegrationEnv(t)
	w, other, user, project := testWorker(t)
	id := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := other.flushRoundActivity(ctx, claimed, 0, "stale text"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong-owner flush err=%v, want no-rows guard", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE ai_turns SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := w.flushRoundActivity(ctx, claimed, 0, "stale text"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired-lease flush err=%v, want no-rows guard", err)
	}
	if err := w.closeAssistantRound(ctx, claimed, 0, "activity", "stale text"); err == nil {
		t.Fatal("expired-lease assistant round mutation succeeded, want guard error")
	}
	if err := w.event(ctx, claimed, "phase", map[string]string{"phase": "thinking"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired-lease event err=%v, want no-rows guard", err)
	}
	if err := saveTurnTranscript(ctx, w.pool, claimed, w.cfg.ID, []ai.Message{{Role: ai.RoleAssistant, Content: "x"}}, userQuestionBudgets{}); err == nil {
		t.Fatal("expired-lease transcript save succeeded, want guard error")
	}
	var blocks string
	if err := w.pool.QueryRow(ctx, `SELECT COALESCE(content_blocks::text, '') FROM ai_messages WHERE turn_id = $1 AND role = 'assistant'`, id).Scan(&blocks); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blocks, "stale text") {
		t.Fatalf("stale write reached public blocks: %s", blocks)
	}
	var events, transcripts int
	if err := w.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1), (SELECT count(*) FROM ai_turn_transcripts WHERE turn_id = $1)`, id).Scan(&events, &transcripts); err != nil {
		t.Fatal(err)
	}
	if events != 0 || transcripts != 0 {
		t.Fatalf("stale writes recorded events=%d transcripts=%d, want none", events, transcripts)
	}
}
