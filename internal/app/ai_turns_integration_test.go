package app

import (
	"context"
	"errors"
	"fmt"
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
