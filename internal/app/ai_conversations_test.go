package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestAIConversationCRUDAuthorizationAndPagination(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := queries.WithTx(tx)

	org1, err := q.CreateOrganization(ctx, "conversation-test-org-1")
	if err != nil {
		t.Fatalf("create org 1: %v", err)
	}
	org2, err := q.CreateOrganization(ctx, "conversation-test-org-2")
	if err != nil {
		t.Fatalf("create org 2: %v", err)
	}

	var memberID, outsiderID pgtype.UUID
	for _, user := range []struct {
		subject string
		email   string
		id      *pgtype.UUID
	}{
		{"conversation-member", "conversation-member@example.com", &memberID},
		{"conversation-outsider", "conversation-outsider@example.com", &outsiderID},
	} {
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (auth_provider, auth_subject, email)
			VALUES ('test', $1, $2) RETURNING id`, user.subject, user.email).Scan(user.id); err != nil {
			t.Fatalf("create user %s: %v", user.subject, err)
		}
	}
	if _, err := q.AddOrganizationMember(ctx, sqlc.AddOrganizationMemberParams{OrgID: org1.ID, UserID: memberID, Role: "owner"}); err != nil {
		t.Fatalf("add member to org 1: %v", err)
	}
	if _, err := q.AddOrganizationMember(ctx, sqlc.AddOrganizationMemberParams{OrgID: org2.ID, UserID: outsiderID, Role: "owner"}); err != nil {
		t.Fatalf("add outsider to org 2: %v", err)
	}

	var project1, project2 pgtype.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'conversation-project-1', 'https://one.example') RETURNING id`, org1.ID).Scan(&project1); err != nil {
		t.Fatalf("create project 1: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, 'conversation-project-2', 'https://two.example') RETURNING id`, org2.ID).Scan(&project2); err != nil {
		t.Fatalf("create project 2: %v", err)
	}

	conversations := make([]sqlc.AiConversation, 0, 3)
	for i := 0; i < 3; i++ {
		conversation, err := q.CreateAIConversationForUser(ctx, sqlc.CreateAIConversationForUserParams{
			ProjectID: project1,
			UserID:    memberID,
		})
		if err != nil {
			t.Fatalf("create conversation %d: %v", i, err)
		}
		conversations = append(conversations, conversation)
		if _, err := tx.Exec(ctx, `
			UPDATE ai_conversations
			SET updated_at = now() + ($2::int * interval '1 second')
			WHERE id = $1`, conversation.ID, i); err != nil {
			t.Fatalf("set conversation timestamp %d: %v", i, err)
		}
	}

	created, err := q.GetAIConversationByIDForUser(ctx, sqlc.GetAIConversationByIDForUserParams{
		ConversationID: conversations[2].ID,
		UserID:         memberID,
	})
	if err != nil {
		t.Fatalf("get created conversation: %v", err)
	}
	if created.Title != "New conversation" || created.ProjectID != project1 || created.CreatedByUserID != memberID || !created.CreatedAt.Valid || !created.UpdatedAt.Valid {
		t.Fatalf("conversation metadata/defaults = %+v", created)
	}

	total, err := q.CountAIConversationsForProjectForUser(ctx, sqlc.CountAIConversationsForProjectForUserParams{ProjectID: project1, UserID: memberID})
	if err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if total != 3 {
		t.Fatalf("count = %d, want 3", total)
	}
	page, err := q.ListAIConversationsForProjectForUser(ctx, sqlc.ListAIConversationsForProjectForUserParams{
		ProjectID: project1, UserID: memberID, PageLimit: 2, PageOffset: 1,
	})
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(page) != 2 || page[0].ID != conversations[1].ID || page[1].ID != conversations[0].ID {
		t.Fatalf("page ordering = %v, want second/newest then oldest", page)
	}

	if _, err := q.GetAIConversationByIDForUser(ctx, sqlc.GetAIConversationByIDForUserParams{
		ConversationID: conversations[0].ID, UserID: outsiderID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-org get error = %v, want no rows", err)
	}
	if _, err := q.GetOrganizationFeaturesByConversationID(ctx, sqlc.GetOrganizationFeaturesByConversationIDParams{
		ConversationID: conversations[0].ID, UserID: outsiderID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-org conversation feature lookup error = %v, want no rows", err)
	}
	if _, err := q.GetOrganizationFeaturesByProjectID(ctx, sqlc.GetOrganizationFeaturesByProjectIDParams{
		ProjectID: project1, UserID: outsiderID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-org project feature lookup error = %v, want no rows", err)
	}
	if deleted, err := q.DeleteAIConversationByIDForUser(ctx, sqlc.DeleteAIConversationByIDForUserParams{
		ConversationID: conversations[0].ID, UserID: outsiderID,
	}); err != nil || deleted != 0 {
		t.Fatalf("cross-org delete = rows %d, err %v; want zero rows", deleted, err)
	}
	if _, err := q.CreateAIConversationForUser(ctx, sqlc.CreateAIConversationForUserParams{
		ProjectID: project1, UserID: outsiderID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-org create error = %v, want no rows", err)
	}
	outsiderTotal, err := q.CountAIConversationsForProjectForUser(ctx, sqlc.CountAIConversationsForProjectForUserParams{ProjectID: project1, UserID: outsiderID})
	if err != nil {
		t.Fatalf("cross-org count: %v", err)
	}
	outsiderList, err := q.ListAIConversationsForProjectForUser(ctx, sqlc.ListAIConversationsForProjectForUserParams{
		ProjectID: project1, UserID: outsiderID, PageLimit: 50,
	})
	if err != nil {
		t.Fatalf("cross-org list: %v", err)
	}
	if outsiderTotal != 0 || len(outsiderList) != 0 {
		t.Fatalf("cross-org list leaked data: total=%d rows=%d", outsiderTotal, len(outsiderList))
	}

	emptyTotal, err := q.CountAIConversationsForProjectForUser(ctx, sqlc.CountAIConversationsForProjectForUserParams{ProjectID: project2, UserID: outsiderID})
	if err != nil {
		t.Fatalf("empty project count: %v", err)
	}
	emptyList, err := q.ListAIConversationsForProjectForUser(ctx, sqlc.ListAIConversationsForProjectForUserParams{
		ProjectID: project2, UserID: outsiderID, PageLimit: 50,
	})
	if err != nil {
		t.Fatalf("empty project list: %v", err)
	}
	if emptyTotal != 0 || len(emptyList) != 0 {
		t.Fatalf("empty project leaked data: total=%d rows=%d", emptyTotal, len(emptyList))
	}

	deleted, err := q.DeleteAIConversationByIDForUser(ctx, sqlc.DeleteAIConversationByIDForUserParams{
		ConversationID: conversations[2].ID, UserID: memberID,
	})
	if err != nil || deleted != 1 {
		t.Fatalf("member delete = rows %d, err %v; want one row", deleted, err)
	}
	if _, err := q.GetAIConversationByIDForUser(ctx, sqlc.GetAIConversationByIDForUserParams{
		ConversationID: conversations[2].ID, UserID: memberID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted get error = %v, want no rows", err)
	}
}

func TestAIConversationHistoryIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first user", "none", "history-first", nil)
	if err != nil {
		t.Fatalf("submit first turn: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status = 'completed', started_at = now() - interval '10 seconds', completed_at = now() WHERE id = $1`, first.TurnID); err != nil {
		t.Fatalf("complete first turn: %v", err)
	}
	second, err := fixture.submit(t, fixture.conversationID, "second user", "none", "history-second", nil)
	if err != nil {
		t.Fatalf("submit second turn: %v", err)
	}

	for _, update := range []struct {
		turnID pgtype.UUID
		at     string
	}{
		{first.TurnID, "2024-01-01T00:00:00Z"},
		{second.TurnID, "2024-01-01T00:01:00Z"},
	} {
		if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET created_at = $2 WHERE id = $1`, update.turnID, update.at); err != nil {
			t.Fatalf("set turn timestamp: %v", err)
		}
		if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_messages SET content = CASE WHEN role = 'assistant' THEN 'assistant reply' ELSE content END, created_at = CASE WHEN role = 'user' THEN $2::timestamptz ELSE $2::timestamptz + interval '1 second' END WHERE turn_id = $1`, update.turnID, update.at); err != nil {
			t.Fatalf("set message timestamps: %v", err)
		}
	}

	// Give the first turn one completed tool call so the detail response can
	// replay it onto the assistant message.
	if _, err := fixture.app.DB.Exec(fixture.ctx, `INSERT INTO ai_tool_calls (turn_id, seq, call_id, name, args, status, summary)
		VALUES ($1, 0, 'history-call-1', 'read_issues', '{"limit":25}'::jsonb, 'completed', '25 issues shown (312 matching total)')`, first.TurnID); err != nil {
		t.Fatalf("insert tool call: %v", err)
	}


	response := httptest.NewRecorder()
	fixture.app.handleGetAIConversation(response, conversationRequest(fixture.userID, fixture.conversationID))
	if response.Code != http.StatusOK {
		t.Fatalf("conversation status = %d, body = %s", response.Code, response.Body.String())
	}
	var conversation aiConversationDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&conversation); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if conversation.ID != fixture.conversationID.String() || conversation.ProjectID != fixture.projectID.String() || conversation.CreatedByUserID != fixture.userID.String() || conversation.Title != "first user" || conversation.CreatedAt == "" || conversation.UpdatedAt == "" {
		t.Fatalf("conversation metadata = %+v", conversation)
	}
	wantMessages := []struct {
		id      pgtype.UUID
		role    string
		status  string
		content string
	}{
		{first.UserMessageID, "user", "complete", "first user"},
		{first.AssistantMessageID, "assistant", "pending", "assistant reply"},
		{second.UserMessageID, "user", "complete", "second user"},
		{second.AssistantMessageID, "assistant", "pending", "assistant reply"},
	}
	if len(conversation.Messages) != len(wantMessages) {
		t.Fatalf("message count = %d, want %d", len(conversation.Messages), len(wantMessages))
	}
	for i, want := range wantMessages {
		message := conversation.Messages[i]
		if message.ID != want.id.String() || message.Role != want.role || message.Status != want.status || message.Content != want.content {
			t.Errorf("message %d = %+v, want %+v", i, message, want)
		}
	}

	firstAssistant := conversation.Messages[1]
	if len(firstAssistant.ToolCalls) != 1 {
		t.Fatalf("first assistant tool calls = %+v, want 1", firstAssistant.ToolCalls)
	}
	call := firstAssistant.ToolCalls[0]
	if call.CallID != "history-call-1" || call.Name != "read_issues" || call.Status != "completed" || call.Seq != 0 || call.Summary == "" {
		t.Fatalf("first assistant tool call = %+v", call)
	}
	if string(call.Args) != `{"limit":25}` {
		t.Fatalf("first assistant tool call args = %s", call.Args)
	}
	if firstAssistant.ActivityStartedAt == nil || firstAssistant.ActivityEndedAt == nil || firstAssistant.ActivityEndedAt.Before(*firstAssistant.ActivityStartedAt) {
		t.Fatalf("first assistant activity = %v..%v, want a valid run window", firstAssistant.ActivityStartedAt, firstAssistant.ActivityEndedAt)
	}
	secondAssistant := conversation.Messages[3]
	if len(secondAssistant.ToolCalls) != 0 || secondAssistant.ActivityStartedAt != nil || secondAssistant.ActivityEndedAt != nil {
		t.Fatalf("second assistant activity = toolCalls %+v, %v..%v; want none", secondAssistant.ToolCalls, secondAssistant.ActivityStartedAt, secondAssistant.ActivityEndedAt)
	}


	emptyConversationID := fixture.conversation(t)
	response = httptest.NewRecorder()
	fixture.app.handleGetAIConversation(response, conversationRequest(fixture.userID, emptyConversationID))
	if response.Code != http.StatusOK {
		t.Fatalf("empty conversation status = %d, body = %s", response.Code, response.Body.String())
	}
	var emptyResponse struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&emptyResponse); err != nil {
		t.Fatalf("decode empty conversation: %v", err)
	}
	if string(emptyResponse.Messages) != "[]" {
		t.Fatalf("empty messages = %s, want []", emptyResponse.Messages)
	}

	outsiderID := createObserverUser(t, fixture, "conversation")
	response = httptest.NewRecorder()
	fixture.app.handleGetAIConversation(response, conversationRequest(outsiderID, fixture.conversationID))
	if response.Code != http.StatusNotFound {
		t.Fatalf("outsider status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAIConversationMessageOrderIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	first, err := fixture.submit(t, fixture.conversationID, "first user", "none", "message-order-first", nil)
	if err != nil {
		t.Fatalf("submit first turn: %v", err)
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status = 'completed', completed_at = now() WHERE id = $1`, first.TurnID); err != nil {
		t.Fatalf("complete first turn: %v", err)
	}
	second, err := fixture.submit(t, fixture.conversationID, "second user", "none", "message-order-second", nil)
	if err != nil {
		t.Fatalf("submit second turn: %v", err)
	}

	for _, update := range []struct {
		turnID        pgtype.UUID
		turnCreatedAt string
		userMessageID string
		assistantID   string
	}{
		{first.TurnID, "2024-01-01T00:00:00Z", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000001"},
		{second.TurnID, "2024-01-01T00:01:00Z", "00000000-0000-0000-0000-000000000004", "00000000-0000-0000-0000-000000000003"},
	} {
		if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET created_at = $2::timestamptz WHERE id = $1`, update.turnID, update.turnCreatedAt); err != nil {
			t.Fatalf("set turn timestamp: %v", err)
		}
		if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_messages
			SET id = CASE role WHEN 'user' THEN $2::uuid ELSE $3::uuid END,
				created_at = '2024-01-01T00:00:00Z'::timestamptz
			WHERE turn_id = $1`, update.turnID, update.userMessageID, update.assistantID); err != nil {
			t.Fatalf("set message timestamps and IDs: %v", err)
		}
	}

	messages, err := fixture.queries.ListAIMessagesForConversation(fixture.ctx, fixture.conversationID)
	if err != nil {
		t.Fatalf("list conversation messages: %v", err)
	}
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("message count = %d, want %d", len(messages), len(wantRoles))
	}
	for i, wantRole := range wantRoles {
		if messages[i].Role != wantRole {
			t.Errorf("message %d role = %q, want %q", i, messages[i].Role, wantRole)
		}
	}
}

func conversationRequest(userID, conversationID pgtype.UUID) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("conversationID", conversationID.String())
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, resolvedUserContextKey{}, resolvedUserEntry{user: sqlc.User{ID: userID}})
	return request.WithContext(ctx)
}
