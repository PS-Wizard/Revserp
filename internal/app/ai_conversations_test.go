package app

import (
	"errors"
	"testing"

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
