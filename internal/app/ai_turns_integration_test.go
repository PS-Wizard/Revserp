package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiTurnFixture struct {
	app            *App
	queries        *sqlc.Queries
	ctx            context.Context
	organizationID pgtype.UUID
	userID         pgtype.UUID
	projectID      pgtype.UUID
	conversationID pgtype.UUID
}

func newAITurnFixture(t *testing.T, limit int32, efforts []string) aiTurnFixture {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	name := fmt.Sprintf("ai-turn-test-%d", time.Now().UnixNano())
	org, err := queries.CreateOrganization(ctx, name)
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}

	var userID, projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM organizations WHERE id = $1", org.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
	})
	if _, err := queries.AddOrganizationMember(ctx, sqlc.AddOrganizationMemberParams{OrgID: org.ID, UserID: userID, Role: "owner"}); err != nil {
		t.Fatalf("add organization member: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, $2, 'https://turn.example') RETURNING id`, org.ID, name).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	conversation, err := queries.CreateAIConversationForUser(ctx, sqlc.CreateAIConversationForUserParams{ProjectID: projectID, UserID: userID})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := queries.UpsertOrganizationFeatures(ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID: org.ID, AutoCrawl: true, GscConnector: true, AiChat: true,
		AiMonthlyMessageLimit: limit, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: efforts,
	}); err != nil {
		t.Fatalf("configure organization features: %v", err)
	}
	return aiTurnFixture{
		app: &App{Config: config.Config{}, DB: pool, Queries: queries}, queries: queries, ctx: ctx,
		organizationID: org.ID, userID: userID, projectID: projectID, conversationID: conversation.ID,
	}
}

func (f aiTurnFixture) conversation(t *testing.T) pgtype.UUID {
	t.Helper()
	conversation, err := f.queries.CreateAIConversationForUser(f.ctx, sqlc.CreateAIConversationForUserParams{ProjectID: f.projectID, UserID: f.userID})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return conversation.ID
}

func (f aiTurnFixture) submit(t *testing.T, conversationID pgtype.UUID, content, effort, requestID string, crawlID *string) (aiTurnSubmission, error) {
	t.Helper()
	request, err := acceptAITurnRequest(aiTurnRequest{Content: content, ReasoningEffort: effort, ClientRequestID: requestID, CrawlID: crawlID})
	if err != nil {
		t.Fatalf("accept request: %v", err)
	}
	return f.app.submitAITurn(f.ctx, f.userID, conversationID, request)
}

func (f aiTurnFixture) usage(t *testing.T) int32 {
	t.Helper()
	var usage int32
	err := f.app.DB.QueryRow(f.ctx, `SELECT used_messages FROM ai_workspace_monthly_usage
		WHERE organization_id = $1 AND period_start = date_trunc('month', now() AT TIME ZONE 'UTC')::date`, f.organizationID).Scan(&usage)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return usage
}

func (f aiTurnFixture) completedCrawl(t *testing.T, projectID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var crawlID pgtype.UUID
	if err := f.app.DB.QueryRow(f.ctx, `INSERT INTO crawls (project_id, status, source, config_snapshot, completed_at)
		VALUES ($1, 'completed', 'manual', '{}'::jsonb, now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create completed crawl: %v", err)
	}
	return crawlID
}

func TestAITurnSubmissionIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "exact content", "medium", "same-key", nil)
	if err != nil {
		t.Fatalf("submit turn: %v", err)
	}

	var status, requested, effective, model, promptVersion, userStatus, userContent, assistantStatus, assistantContent string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT t.status, t.requested_effort, t.effective_effort, t.model, t.prompt_version,
		user_message.status, user_message.content, assistant_message.status, assistant_message.content
		FROM ai_turns t
		JOIN ai_messages user_message ON user_message.turn_id = t.id AND user_message.role = 'user'
		JOIN ai_messages assistant_message ON assistant_message.turn_id = t.id AND assistant_message.role = 'assistant'
		WHERE t.id = $1`, first.TurnID).Scan(&status, &requested, &effective, &model, &promptVersion, &userStatus, &userContent, &assistantStatus, &assistantContent); err != nil {
		t.Fatalf("read submitted turn: %v", err)
	}
	if status != "queued" || requested != "high" || effective != "high" || model != defaultAITurnModel || promptVersion != "chat-v1" || userStatus != "complete" || userContent != "exact content" || assistantStatus != "pending" || assistantContent != "" {
		t.Fatalf("submitted turn fields = %q %q %q %q %q %q %q %q %q", status, requested, effective, model, promptVersion, userStatus, userContent, assistantStatus, assistantContent)
	}
	if got := fixture.usage(t); got != 1 {
		t.Fatalf("usage = %d, want 1", got)
	}

	retry, err := fixture.submit(t, fixture.conversationID, "exact content", "high", "same-key", nil)
	if err != nil {
		t.Fatalf("retry turn: %v", err)
	}
	got := fixture.usage(t)
	if retry != first || got != 1 {
		t.Fatalf("retry = %+v usage = %d, want same IDs and one charge", retry, got)
	}
	if _, err := fixture.submit(t, fixture.conversationID, "changed", "high", "same-key", nil); err != errIdempotencyConflict {
		t.Fatalf("changed idempotency request error = %v, want %v", err, errIdempotencyConflict)
	}
	if _, err := fixture.submit(t, fixture.conversationID, "next", "high", "next-key", nil); err != errConversationBusy {
		t.Fatalf("busy conversation error = %v, want %v", err, errConversationBusy)
	}
	if got := fixture.usage(t); got != 1 {
		t.Fatalf("usage after rejected requests = %d, want 1", got)
	}

	if err := fixture.queries.UpsertOrganizationFeatures(fixture.ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID: fixture.organizationID, AutoCrawl: true, GscConnector: true, AiChat: true,
		AiMonthlyMessageLimit: 10, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: []string{"none"},
	}); err != nil {
		t.Fatalf("restrict reasoning: %v", err)
	}
	if _, err := fixture.submit(t, fixture.conversation(t), "not allowed", "high", "reasoning-key", nil); err != errReasoningNotAllowed {
		t.Fatalf("reasoning entitlement error = %v, want %v", err, errReasoningNotAllowed)
	}
	if got := fixture.usage(t); got != 1 {
		t.Fatalf("usage after reasoning rejection = %d, want 1", got)
	}
	if err := fixture.queries.UpsertOrganizationFeatures(fixture.ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID: fixture.organizationID, AutoCrawl: true, GscConnector: true, AiChat: false,
		AiMonthlyMessageLimit: 10, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: canonicalAIReasoningEfforts,
	}); err != nil {
		t.Fatalf("disable ai chat: %v", err)
	}
	if _, err := fixture.submit(t, fixture.conversation(t), "disabled", "low", "disabled-key", nil); err != errAIChatDisabled {
		t.Fatalf("disabled chat error = %v, want %v", err, errAIChatDisabled)
	}
	if got := fixture.usage(t); got != 1 {
		t.Fatalf("usage after disabled rejection = %d, want 1", got)
	}
}

func TestAITurnSubmissionCrawlSelection(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	validCrawl := fixture.completedCrawl(t, fixture.projectID)
	valid := validCrawl.String()
	validSubmission, err := fixture.submit(t, fixture.conversationID, "with crawl", "low", "valid-crawl", &valid)
	if err != nil {
		t.Fatalf("submit valid crawl: %v", err)
	}
	var stored pgtype.UUID
	if err := fixture.app.DB.QueryRow(fixture.ctx, "SELECT crawl_id FROM ai_turns WHERE id = $1", validSubmission.TurnID).Scan(&stored); err != nil || stored != validCrawl {
		t.Fatalf("stored valid crawl = %v, %v; want %v", stored, err, validCrawl)
	}

	var otherProject pgtype.UUID
	if err := fixture.app.DB.QueryRow(fixture.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'other-turn-project', 'https://other.example') RETURNING id`, fixture.organizationID).Scan(&otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	crossProjectCrawl := fixture.completedCrawl(t, otherProject).String()
	if _, err := fixture.submit(t, fixture.conversation(t), "bad crawl", "low", "cross-crawl", &crossProjectCrawl); err != errInvalidCrawl {
		t.Fatalf("cross project crawl error = %v, want %v", err, errInvalidCrawl)
	}

	latestCrawl := fixture.completedCrawl(t, fixture.projectID)
	latestSubmission, err := fixture.submit(t, fixture.conversation(t), "latest crawl", "low", "latest-crawl", nil)
	if err != nil {
		t.Fatalf("submit latest crawl: %v", err)
	}
	if err := fixture.app.DB.QueryRow(fixture.ctx, "SELECT crawl_id FROM ai_turns WHERE id = $1", latestSubmission.TurnID).Scan(&stored); err != nil || stored != latestCrawl {
		t.Fatalf("stored latest crawl = %v, %v; want %v", stored, err, latestCrawl)
	}
	if got := fixture.usage(t); got != 2 {
		t.Fatalf("usage after valid, invalid, and latest crawl requests = %d, want 2", got)
	}
}

func TestAITurnSubmissionSharedQuotaIsAtomic(t *testing.T) {
	fixture := newAITurnFixture(t, 1, canonicalAIReasoningEfforts)
	conversations := []pgtype.UUID{fixture.conversationID, fixture.conversation(t)}
	start := make(chan struct{})
	errs := make(chan error, len(conversations))
	var wg sync.WaitGroup
	for i, conversationID := range conversations {
		wg.Add(1)
		go func(i int, conversationID pgtype.UUID) {
			defer wg.Done()
			<-start
			request, err := acceptAITurnRequest(aiTurnRequest{Content: fmt.Sprintf("message %d", i), ReasoningEffort: "low", ClientRequestID: fmt.Sprintf("concurrent-%d", i)})
			if err == nil {
				_, err = fixture.app.submitAITurn(context.Background(), fixture.userID, conversationID, request)
			}
			errs <- err
		}(i, conversationID)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes, limited := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errMonthlyMessageLimit):
			limited++
		default:
			t.Fatalf("concurrent submission error: %v", err)
		}
	}
	if successes != 1 || limited != 1 || fixture.usage(t) != 1 {
		t.Fatalf("successes=%d limited=%d usage=%d, want 1/1/1", successes, limited, fixture.usage(t))
	}
}

func TestAITurnSubmissionSnapshotsDisabledTools(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	if err := fixture.queries.UpsertOrganizationFeatures(fixture.ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID: fixture.organizationID, AutoCrawl: true, GscConnector: true, AiChat: true,
		AiMonthlyMessageLimit: 10, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: canonicalAIReasoningEfforts,
		DisabledAiTools: []string{"read_issues", "unknown", "", "read_issues"},
	}); err != nil {
		t.Fatalf("configure disabled tools: %v", err)
	}
	submission, err := fixture.submit(t, fixture.conversation(t), "denylist", "low", "denylist-key", nil)
	if err != nil {
		t.Fatalf("submit turn: %v", err)
	}
	var stored []string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT disabled_ai_tools FROM ai_turns WHERE id = $1`, submission.TurnID).Scan(&stored); err != nil {
		t.Fatalf("read disabled tools: %v", err)
	}
	if len(stored) != 1 || stored[0] != "read_issues" {
		t.Fatalf("stored disabled tools = %v, want [read_issues]", stored)
	}

	// the snapshot is frozen at creation: a later workspace change must not mutate it
	if err := fixture.queries.UpsertOrganizationFeatures(fixture.ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID: fixture.organizationID, AutoCrawl: true, GscConnector: true, AiChat: true,
		AiMonthlyMessageLimit: 10, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: canonicalAIReasoningEfforts,
		DisabledAiTools: []string{},
	}); err != nil {
		t.Fatalf("re-enable tools: %v", err)
	}
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT disabled_ai_tools FROM ai_turns WHERE id = $1`, submission.TurnID).Scan(&stored); err != nil {
		t.Fatalf("re-read disabled tools: %v", err)
	}
	if len(stored) != 1 || stored[0] != "read_issues" {
		t.Fatalf("snapshot mutated after workspace change: %v", stored)
	}
}

func TestAITurnDetailIncludesToolCalls(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	submission := submitObserverTurn(t, fixture, fixture.conversationID, "tools", "tools-detail")
	if _, err := fixture.app.DB.Exec(fixture.ctx, `INSERT INTO ai_tool_calls (turn_id, seq, call_id, name, args, status, summary) VALUES ($1, 1, 'call-1', 'read_issues', '{"limit":5}'::jsonb, 'completed', '5 issues shown (7 matching total)')`, submission.TurnID); err != nil {
		t.Fatalf("insert tool call: %v", err)
	}
	response := httptest.NewRecorder()
	fixture.app.handleGetAITurn(response, observerRequest(fixture.userID, http.MethodGet, "/", submission.TurnID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", response.Code, response.Body.String())
	}
	turn := decodeAITurnResponse(t, response)
	if len(turn.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", turn.ToolCalls)
	}
	call := turn.ToolCalls[0]
	if call.CallID != "call-1" || call.Name != "read_issues" || call.Status != "completed" || call.Summary != "5 issues shown (7 matching total)" || call.Seq != 1 || string(call.Args) != `{"limit":5}` {
		t.Fatalf("tool call = %+v", call)
	}
	if call.CreatedAt.IsZero() {
		t.Fatalf("tool call created_at missing: %+v", call)
	}
}

func TestAITurnDetailWithoutToolCallsReturnsEmptyArray(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	submission := submitObserverTurn(t, fixture, fixture.conversationID, "plain", "plain-detail")
	response := httptest.NewRecorder()
	fixture.app.handleGetAITurn(response, observerRequest(fixture.userID, http.MethodGet, "/", submission.TurnID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", response.Code, response.Body.String())
	}
	turn := decodeAITurnResponse(t, response)
	if len(turn.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want []", turn.ToolCalls)
	}
}

func TestAITurnSubmissionRecoversExpiredNoOutputRequeuesAndBlocksNewTurn(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first", "low", "first-key", nil)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', attempt_count=1, lease_expires_at=now() - interval '1 second', heartbeat_at=now() - interval '1 second', started_at=now(), output_started_at=NULL WHERE id=$1`, first.TurnID); err != nil {
		t.Fatalf("expire turn: %v", err)
	}
	var beforeQueuedAt time.Time
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT queued_at FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&beforeQueuedAt); err != nil {
		t.Fatalf("read queued_at: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	_, err = fixture.submit(t, fixture.conversationID, "second", "low", "second-key", nil)
	if !errors.Is(err, errConversationBusy) {
		t.Fatalf("second submit error = %v, want %v", err, errConversationBusy)
	}
	var status, errCode string
	var claimedBy pgtype.Text
	var leaseExpiresAt, heartbeatAt, completedAt pgtype.Timestamptz
	var queuedAt time.Time
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, claimed_by, lease_expires_at, heartbeat_at, completed_at, COALESCE(error_code,''), queued_at FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&status, &claimedBy, &leaseExpiresAt, &heartbeatAt, &completedAt, &errCode, &queuedAt); err != nil {
		t.Fatalf("read recovered turn: %v", err)
	}
	if status != "queued" || claimedBy.Valid || leaseExpiresAt.Valid || heartbeatAt.Valid || completedAt.Valid || errCode != "" {
		t.Fatalf("requeued turn = status=%q claimed=%v lease=%v heartbeat=%v completed=%v err=%q queued=%v", status, claimedBy, leaseExpiresAt, heartbeatAt, completedAt, errCode, queuedAt)
	}
	if !queuedAt.After(beforeQueuedAt) {
		t.Fatalf("queued_at not bumped: before=%v after=%v", beforeQueuedAt, queuedAt)
	}
	var msgStatus string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status FROM ai_messages WHERE turn_id=$1 AND role='assistant'`, first.TurnID).Scan(&msgStatus); err != nil || msgStatus != "pending" {
		t.Fatalf("assistant message status=%q err=%v want pending", msgStatus, err)
	}
	var failedEvents int
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id=$1 AND event_type='failed'`, first.TurnID).Scan(&failedEvents); err != nil || failedEvents != 0 {
		t.Fatalf("failed events=%d err=%v want 0", failedEvents, err)
	}
	busy, err := fixture.queries.HasActiveAITurnForConversation(fixture.ctx, fixture.conversationID)
	if err != nil || !busy {
		t.Fatalf("HasActive after requeue = %v err=%v want true", busy, err)
	}
	var queuedCount int
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turns WHERE conversation_id=$1 AND status='queued'`, fixture.conversationID).Scan(&queuedCount); err != nil || queuedCount != 1 {
		t.Fatalf("queued count=%d err=%v want 1", queuedCount, err)
	}
}

func TestAITurnSubmissionRecoversExpiredPartialFailsAndAllowsNewTurn(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first", "low", "first-key", nil)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_messages SET status='partial', content='partial output', updated_at=now() WHERE turn_id=$1 AND role='assistant'`, first.TurnID); err != nil {
		t.Fatalf("mark partial message: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', attempt_count=1, lease_expires_at=now() - interval '1 second', heartbeat_at=now() - interval '1 second', started_at=now(), output_started_at=now() WHERE id=$1`, first.TurnID); err != nil {
		t.Fatalf("expire partial turn: %v", err)
	}
	second, err := fixture.submit(t, fixture.conversationID, "second", "low", "second-key", nil)
	if err != nil {
		t.Fatalf("submit second after partial expired: %v", err)
	}
	var status, errCode string
	var claimedBy pgtype.Text
	var leaseExpiresAt, heartbeatAt pgtype.Timestamptz
	var completedAt pgtype.Timestamptz
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, claimed_by, lease_expires_at, heartbeat_at, completed_at, COALESCE(error_code,'') FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&status, &claimedBy, &leaseExpiresAt, &heartbeatAt, &completedAt, &errCode); err != nil {
		t.Fatalf("read failed turn: %v", err)
	}
	if status != "failed" || claimedBy.Valid || leaseExpiresAt.Valid || heartbeatAt.Valid || !completedAt.Valid || errCode != "worker_interrupted" {
		t.Fatalf("failed partial turn = status=%q claimed=%v lease=%v heartbeat=%v completed=%v err=%q", status, claimedBy, leaseExpiresAt, heartbeatAt, completedAt, errCode)
	}
	var msgStatus, msgContent string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, content FROM ai_messages WHERE turn_id=$1 AND role='assistant'`, first.TurnID).Scan(&msgStatus, &msgContent); err != nil || msgStatus != "partial" || msgContent != "partial output" {
		t.Fatalf("assistant message after partial fail = %q %q err=%v", msgStatus, msgContent, err)
	}
	var failedEvents int
	var payload string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*), COALESCE(MIN(payload::text),'') FROM ai_turn_events WHERE turn_id=$1 AND event_type='failed'`, first.TurnID).Scan(&failedEvents, &payload); err != nil {
		t.Fatalf("read events: %v", err)
	}
	if failedEvents != 1 || payload != `{"error_code": "worker_interrupted"}` && payload != `{"error_code":"worker_interrupted"}` {
		t.Fatalf("failed events=%d payload=%q want 1 with worker_interrupted", failedEvents, payload)
	}
	var newStatus string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status FROM ai_turns WHERE id=$1`, second.TurnID).Scan(&newStatus); err != nil || newStatus != "queued" {
		t.Fatalf("new turn status=%q err=%v want queued", newStatus, err)
	}
}

func TestAITurnSubmissionRecoversExpiredExhaustedFailsAndAllowsNewTurn(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first", "low", "first-key", nil)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', attempt_count=2, lease_expires_at=now() - interval '1 second', heartbeat_at=now() - interval '1 second', started_at=now(), output_started_at=NULL WHERE id=$1`, first.TurnID); err != nil {
		t.Fatalf("expire exhausted turn: %v", err)
	}
	second, err := fixture.submit(t, fixture.conversationID, "second", "low", "second-key", nil)
	if err != nil {
		t.Fatalf("submit second after exhausted: %v", err)
	}
	var status, errCode string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, COALESCE(error_code,'') FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&status, &errCode); err != nil || status != "failed" || errCode != "worker_interrupted" {
		t.Fatalf("exhausted turn = %q %q err=%v", status, errCode, err)
	}
	var msgStatus string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status FROM ai_messages WHERE turn_id=$1 AND role='assistant'`, first.TurnID).Scan(&msgStatus); err != nil || msgStatus != "failed" {
		t.Fatalf("assistant message status=%q err=%v want failed", msgStatus, err)
	}
	var failedEvents int
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id=$1 AND event_type='failed'`, first.TurnID).Scan(&failedEvents); err != nil || failedEvents != 1 {
		t.Fatalf("failed events=%d err=%v want 1", failedEvents, err)
	}
	var newStatus string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status FROM ai_turns WHERE id=$1`, second.TurnID).Scan(&newStatus); err != nil || newStatus != "queued" {
		t.Fatalf("new turn status=%q err=%v want queued", newStatus, err)
	}
}

func TestAITurnSubmissionDoesNotRecoverUnexpiredRunning(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first", "low", "first-key", nil)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status='running', claimed_by='live-worker', attempt_count=1, lease_expires_at=now() + interval '1 minute', heartbeat_at=now(), started_at=now(), output_started_at=NULL WHERE id=$1`, first.TurnID); err != nil {
		t.Fatalf("make running live: %v", err)
	}
	_, err = fixture.submit(t, fixture.conversationID, "second", "low", "second-key", nil)
	if !errors.Is(err, errConversationBusy) {
		t.Fatalf("second submit error = %v, want %v", err, errConversationBusy)
	}
	var status, claimedBy string
	var leaseExpiresAt pgtype.Timestamptz
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, COALESCE(claimed_by,''), lease_expires_at FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&status, &claimedBy, &leaseExpiresAt); err != nil {
		t.Fatalf("read turn: %v", err)
	}
	if status != "running" || claimedBy != "live-worker" || !leaseExpiresAt.Valid || leaseExpiresAt.Time.Before(time.Now()) {
		t.Fatalf("unexpired turn mutated: status=%q claimed=%q lease=%v", status, claimedBy, leaseExpiresAt)
	}
	var failedEvents int
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id=$1 AND event_type='failed'`, first.TurnID).Scan(&failedEvents); err != nil || failedEvents != 0 {
		t.Fatalf("failed events=%d err=%v want 0", failedEvents, err)
	}
}

func TestAITurnSubmissionDoesNotRecoverNullLease(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first", "low", "first-key", nil)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', attempt_count=1, lease_expires_at=NULL, heartbeat_at=NULL, started_at=now(), output_started_at=NULL WHERE id=$1`, first.TurnID); err != nil {
		t.Fatalf("make running null lease: %v", err)
	}
	_, err = fixture.submit(t, fixture.conversationID, "second", "low", "second-key", nil)
	if !errors.Is(err, errConversationBusy) {
		t.Fatalf("second submit error = %v, want %v", err, errConversationBusy)
	}
	var status string
	var leaseExpiresAt pgtype.Timestamptz
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, lease_expires_at FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&status, &leaseExpiresAt); err != nil || status != "running" || leaseExpiresAt.Valid {
		t.Fatalf("null lease turn mutated: status=%q lease=%v err=%v", status, leaseExpiresAt, err)
	}
}

func TestAITurnSubmissionRecoversExpiredConcurrentlyWithoutDuplicateEvent(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first", "low", "first-key", nil)
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_messages SET status='partial', content='partial', updated_at=now() WHERE turn_id=$1 AND role='assistant'`, first.TurnID); err != nil {
		t.Fatalf("mark partial: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', attempt_count=1, lease_expires_at=now() - interval '1 second', heartbeat_at=now() - interval '1 second', started_at=now(), output_started_at=now() WHERE id=$1`, first.TurnID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	ids := make(chan pgtype.UUID, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			request, _ := acceptAITurnRequest(aiTurnRequest{Content: fmt.Sprintf("concurrent %d", i), ReasoningEffort: "low", ClientRequestID: fmt.Sprintf("concurrent-expired-%d-%d", time.Now().UnixNano(), i)})
			sub, err := fixture.app.submitAITurn(context.Background(), fixture.userID, fixture.conversationID, request)
			errs <- err
			if err == nil {
				ids <- sub.TurnID
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(ids)
	successes, busy := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errConversationBusy):
			busy++
		default:
			t.Fatalf("concurrent submit error: %v", err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("successes=%d busy=%d want 1/1", successes, busy)
	}
	var failedStatus, errCode string
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, COALESCE(error_code,'') FROM ai_turns WHERE id=$1`, first.TurnID).Scan(&failedStatus, &errCode); err != nil || failedStatus != "failed" {
		t.Fatalf("first turn status=%q err=%v want failed", failedStatus, err)
	}
	var failedEvents int
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id=$1 AND event_type='failed'`, first.TurnID).Scan(&failedEvents); err != nil || failedEvents != 1 {
		t.Fatalf("failed events=%d err=%v want 1", failedEvents, err)
	}
}

func TestAITurnSubmissionConcurrentCreateEnforcesSingleActive(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			request, _ := acceptAITurnRequest(aiTurnRequest{Content: fmt.Sprintf("race %d", i), ReasoningEffort: "low", ClientRequestID: fmt.Sprintf("race-%d-%d", time.Now().UnixNano(), i)})
			_, err := fixture.app.submitAITurn(context.Background(), fixture.userID, fixture.conversationID, request)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, busy := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errConversationBusy):
			busy++
		default:
			if isAITurnActiveUniqueError(err) {
				t.Fatalf("unique violation leaked as raw error: %v", err)
			}
			t.Fatalf("concurrent create error: %v", err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("successes=%d busy=%d want 1/1", successes, busy)
	}
	var activeCount int
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turns WHERE conversation_id=$1 AND status IN ('queued','running')`, fixture.conversationID).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatalf("active count=%d err=%v want 1", activeCount, err)
	}
}
