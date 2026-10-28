package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type contentDraftFixture struct {
	app       *App
	queries   *sqlc.Queries
	ctx       context.Context
	orgID     pgtype.UUID
	userID    pgtype.UUID
	projectID pgtype.UUID
}

func newContentDraftFixture(t *testing.T) contentDraftFixture {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	name := fmt.Sprintf("content-draft-test-%d", time.Now().UnixNano())
	org, err := queries.CreateOrganization(ctx, name)
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}

	var userID, outsiderID pgtype.UUID
	for _, user := range []struct {
		subject string
		email   string
		id      *pgtype.UUID
	}{
		{"content-draft-member", name + "-member@example.com", &userID},
		{"content-draft-outsider", name + "-outsider@example.com", &outsiderID},
	} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
			VALUES ('test', $1, $2) RETURNING id`, user.subject, user.email).Scan(user.id); err != nil {
			t.Fatalf("create user %s: %v", user.subject, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM organizations WHERE id = $1", org.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", outsiderID)
	})
	if _, err := queries.AddOrganizationMember(ctx, sqlc.AddOrganizationMemberParams{OrgID: org.ID, UserID: userID, Role: "owner"}); err != nil {
		t.Fatalf("add organization member: %v", err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1, $2, 'https://draft.example') RETURNING id`, org.ID, name).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return contentDraftFixture{
		app: &App{DB: pool, Queries: queries}, queries: queries, ctx: ctx,
		orgID: org.ID, userID: userID, projectID: projectID,
	}
}

func (f contentDraftFixture) outsiderID(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := f.app.DB.QueryRow(f.ctx, `SELECT id FROM users WHERE auth_subject = 'content-draft-outsider'`).Scan(&id); err != nil {
		t.Fatalf("find outsider: %v", err)
	}
	return id
}

func TestContentDraftQueryLifecycle(t *testing.T) {
	fixture := newContentDraftFixture(t)
	ctx, q := fixture.ctx, fixture.queries

	draft, err := q.CreateContentDraftForUser(ctx, sqlc.CreateContentDraftForUserParams{
		UserID:    fixture.userID,
		ProjectID: fixture.projectID,
		Title:     pgtype.Text{String: "Launch post", Valid: true},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if draft.Title != "Launch post" || draft.ProjectID != fixture.projectID || draft.CreatedByUserID != fixture.userID {
		t.Fatalf("draft metadata = %+v", draft)
	}

	conversation, err := q.CreateAIConversationForContentDraft(ctx, sqlc.CreateAIConversationForContentDraftParams{
		UserID:         fixture.userID,
		ProjectID:      fixture.projectID,
		Title:          "Launch post",
		ContentDraftID: draft.ID,
	})
	if err != nil {
		t.Fatalf("create draft conversation: %v", err)
	}
	if conversation.ProjectID != fixture.projectID || !conversation.ContentDraftID.Valid || conversation.ContentDraftID != draft.ID {
		t.Fatalf("draft conversation = %+v", conversation)
	}

	// A second draft conversation for the same draft violates the one-chat rule.
	if _, err := q.CreateAIConversationForContentDraft(ctx, sqlc.CreateAIConversationForContentDraftParams{
		UserID:         fixture.userID,
		ProjectID:      fixture.projectID,
		Title:          "duplicate",
		ContentDraftID: draft.ID,
	}); err == nil {
		t.Fatalf("second conversation for draft succeeded, want unique violation")
	}

	fetched, err := q.GetContentDraftForUser(ctx, sqlc.GetContentDraftForUserParams{
		DraftID: draft.ID,
		UserID:  fixture.userID,
	})
	if err != nil {
		t.Fatalf("get draft: %v", err)
	}
	if fetched.ID != draft.ID || !fetched.ConversationID.Valid || fetched.ConversationID != conversation.ID {
		t.Fatalf("get draft conversation = %+v, want %v", fetched.ConversationID, conversation.ID)
	}

	// Normal conversation list/count must exclude the draft-scoped conversation.
	normal, err := q.CreateAIConversationForUser(ctx, sqlc.CreateAIConversationForUserParams{
		ProjectID: fixture.projectID,
		UserID:    fixture.userID,
	})
	if err != nil {
		t.Fatalf("create normal conversation: %v", err)
	}
	total, err := q.CountAIConversationsForProjectForUser(ctx, sqlc.CountAIConversationsForProjectForUserParams{
		ProjectID: fixture.projectID,
		UserID:    fixture.userID,
	})
	if err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if total != 1 {
		t.Fatalf("count = %d, want 1 (draft chat excluded)", total)
	}
	listed, err := q.ListAIConversationsForProjectForUser(ctx, sqlc.ListAIConversationsForProjectForUserParams{
		ProjectID: fixture.projectID, UserID: fixture.userID, PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != normal.ID {
		t.Fatalf("listed = %+v, want only the normal conversation", listed)
	}
	got, err := q.GetAIConversationByIDForUser(ctx, sqlc.GetAIConversationByIDForUserParams{
		ConversationID: conversation.ID,
		UserID:         fixture.userID,
	})
	if err != nil {
		t.Fatalf("get draft conversation by id: %v", err)
	}
	if !got.ContentDraftID.Valid || got.ContentDraftID != draft.ID {
		t.Fatalf("draft conversation content_draft_id = %+v", got.ContentDraftID)
	}

	// Partial update keeps untouched columns and always bumps updated_at.
	if _, err := q.UpdateContentDraftForUser(ctx, sqlc.UpdateContentDraftForUserParams{
		DraftID: draft.ID,
		UserID:  fixture.userID,
		Body:    pgtype.Text{String: "# Hello", Valid: true},
	}); err != nil {
		t.Fatalf("partial update: %v", err)
	}
	after, err := q.GetContentDraftForUser(ctx, sqlc.GetContentDraftForUserParams{
		DraftID: draft.ID,
		UserID:  fixture.userID,
	})
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.Body != "# Hello" || after.Title != "Launch post" || after.MetaTitle != "" {
		t.Fatalf("partial update leaked: %+v", after)
	}

	if _, err := fixture.app.DB.Exec(ctx, `UPDATE content_drafts SET updated_at = now() - interval '1 hour' WHERE id = $1`, draft.ID); err != nil {
		t.Fatalf("backdate draft: %v", err)
	}
	before, err := q.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{
		ID:        draft.ID,
		ProjectID: fixture.projectID,
	})
	if err != nil {
		t.Fatalf("project-scoped get: %v", err)
	}
	updated, err := q.UpdateContentDraftForProject(ctx, sqlc.UpdateContentDraftForProjectParams{
		ID:        draft.ID,
		ProjectID: fixture.projectID,
		MetaTitle: pgtype.Text{String: "Meta", Valid: true},
	})
	if err != nil {
		t.Fatalf("project-scoped update: %v", err)
	}
	if updated.MetaTitle != "Meta" || updated.Title != "Launch post" || updated.Body != "# Hello" {
		t.Fatalf("project-scoped update = %+v", updated)
	}
	if !updated.UpdatedAt.Time.After(before.UpdatedAt.Time) {
		t.Fatalf("updated_at not bumped: %v -> %v", before.UpdatedAt.Time, updated.UpdatedAt.Time)
	}
	if _, err := q.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{
		ID:        draft.ID,
		ProjectID: pgtype.UUID{},
	}); err == nil {
		t.Fatalf("cross-project get succeeded, want no rows")
	}
	if _, err := q.UpdateContentDraftForProject(ctx, sqlc.UpdateContentDraftForProjectParams{
		ID:        draft.ID,
		ProjectID: pgtype.UUID{},
		Body:      pgtype.Text{String: "x", Valid: true},
	}); err == nil {
		t.Fatalf("cross-project update succeeded, want no rows")
	}

	// Cross-org isolation.
	outsider := fixture.outsiderID(t)
	if _, err := q.GetContentDraftForUser(ctx, sqlc.GetContentDraftForUserParams{
		DraftID: draft.ID,
		UserID:  outsider,
	}); err == nil {
		t.Fatalf("outsider get succeeded, want no rows")
	}
	if _, err := q.CreateContentDraftForUser(ctx, sqlc.CreateContentDraftForUserParams{
		UserID:    outsider,
		ProjectID: fixture.projectID,
	}); err == nil {
		t.Fatalf("outsider create succeeded, want no rows")
	}
	if deleted, err := q.DeleteContentDraftForUser(ctx, sqlc.DeleteContentDraftForUserParams{
		DraftID: draft.ID,
		UserID:  outsider,
	}); err != nil || deleted != 0 {
		t.Fatalf("outsider delete = %d, %v; want zero rows", deleted, err)
	}

	// Delete cascades the attached conversation.
	deleted, err := q.DeleteContentDraftForUser(ctx, sqlc.DeleteContentDraftForUserParams{
		DraftID: draft.ID,
		UserID:  fixture.userID,
	})
	if err != nil || deleted != 1 {
		t.Fatalf("delete = %d, %v; want one row", deleted, err)
	}
	if _, err := q.GetAIConversationByIDForUser(ctx, sqlc.GetAIConversationByIDForUserParams{
		ConversationID: conversation.ID,
		UserID:         fixture.userID,
	}); err == nil {
		t.Fatalf("draft conversation survived draft delete")
	}
}

func draftRequest(t *testing.T, method, target string, userID pgtype.UUID, params map[string]string, body string) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	routeContext := chi.NewRouteContext()
	for key, value := range params {
		routeContext.URLParams.Add(key, value)
	}
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return request.WithContext(ctx)
}

func TestContentDraftHTTPHandlers(t *testing.T) {
	fixture := newContentDraftFixture(t)

	// Create with title.
	recorder := httptest.NewRecorder()
	fixture.app.handleCreateContentDraft(recorder, draftRequest(t, http.MethodPost, "/", fixture.userID,
		map[string]string{"projectID": fixture.projectID.String()}, `{"title":"  Launch post  "}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var created contentDraftResponse
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Title != "Launch post" || created.ProjectID != fixture.projectID.String() || created.ConversationID == nil || *created.ConversationID == "" {
		t.Fatalf("created draft = %+v", created)
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("created draft missing timestamps: %+v", created)
	}

	// Create without body defaults the title; the conversation still gets one.
	recorder = httptest.NewRecorder()
	fixture.app.handleCreateContentDraft(recorder, draftRequest(t, http.MethodPost, "/", fixture.userID,
		map[string]string{"projectID": fixture.projectID.String()}, ""))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("empty-body create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var untitled contentDraftResponse
	if err := json.NewDecoder(recorder.Body).Decode(&untitled); err != nil {
		t.Fatalf("decode untitled: %v", err)
	}
	if untitled.Title != "" || untitled.ConversationID == nil {
		t.Fatalf("untitled draft = %+v", untitled)
	}

	// Unknown fields are rejected.
	recorder = httptest.NewRecorder()
	fixture.app.handleCreateContentDraft(recorder, draftRequest(t, http.MethodPost, "/", fixture.userID,
		map[string]string{"projectID": fixture.projectID.String()}, `{"nope":1}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, want 400", recorder.Code)
	}

	// List shape.
	recorder = httptest.NewRecorder()
	fixture.app.handleListContentDrafts(recorder, draftRequest(t, http.MethodGet, "/", fixture.userID,
		map[string]string{"projectID": fixture.projectID.String()}, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var listed struct {
		Drafts []struct {
			ID             string  `json:"id"`
			Title          string  `json:"title"`
			TargetKeyword  string  `json:"target_keyword"`
			ConversationID *string `json:"conversation_id"`
			UpdatedAt      string  `json:"updated_at"`
		} `json:"drafts"`
		Raw json.RawMessage `json:"-"`
	}
	rawList := recorder.Body.String()
	if err := json.Unmarshal([]byte(rawList), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Drafts) != 2 {
		t.Fatalf("list count = %d, want 2; body = %s", len(listed.Drafts), rawList)
	}
	var extraKeys []string
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawList), &decoded); err != nil {
		t.Fatalf("decode list envelope: %v", err)
	}
	for key := range decoded {
		if key != "drafts" {
			extraKeys = append(extraKeys, key)
		}
	}
	if len(extraKeys) != 0 {
		t.Fatalf("list envelope keys = %v, want only drafts", extraKeys)
	}

	// Get + partial PATCH.
	recorder = httptest.NewRecorder()
	fixture.app.handleGetContentDraft(recorder, draftRequest(t, http.MethodGet, "/", fixture.userID,
		map[string]string{"draftID": created.ID}, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var fetched contentDraftResponse
	if err := json.NewDecoder(recorder.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched: %v", err)
	}
	if fetched.ID != created.ID || fetched.ConversationID == nil || *fetched.ConversationID != *created.ConversationID {
		t.Fatalf("fetched draft = %+v, want conversation %v", fetched, created.ConversationID)
	}

	recorder = httptest.NewRecorder()
	fixture.app.handleUpdateContentDraft(recorder, draftRequest(t, http.MethodPatch, "/", fixture.userID,
		map[string]string{"draftID": created.ID}, `{"meta_title":"Meta","body":"# Hello"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var patched contentDraftResponse
	if err := json.NewDecoder(recorder.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched: %v", err)
	}
	if patched.MetaTitle != "Meta" || patched.Body != "# Hello" || patched.Title != "Launch post" || patched.TargetKeyword != "" {
		t.Fatalf("patched draft = %+v", patched)
	}

	// The attached conversation carries the draft link on its response.
	recorder = httptest.NewRecorder()
	fixture.app.handleGetAIConversation(recorder, conversationRequest(fixture.userID, mustParseUUID(t, *created.ConversationID)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get draft conversation status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var draftConversation aiConversationResponse
	if err := json.NewDecoder(recorder.Body).Decode(&draftConversation); err != nil {
		t.Fatalf("decode draft conversation: %v", err)
	}
	if draftConversation.ContentDraftID == nil || *draftConversation.ContentDraftID != created.ID {
		t.Fatalf("draft conversation content_draft_id = %+v, want %s", draftConversation.ContentDraftID, created.ID)
	}

	// Outsider visibility.
	outsider := fixture.outsiderID(t)
	for _, call := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		body    string
	}{
		{"get", fixture.app.handleGetContentDraft, http.MethodGet, ""},
		{"patch", fixture.app.handleUpdateContentDraft, http.MethodPatch, `{"title":"x"}`},
	} {
		recorder = httptest.NewRecorder()
		call.handler(recorder, draftRequest(t, call.method, "/", outsider,
			map[string]string{"draftID": created.ID}, call.body))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("outsider %s status = %d, want 404", call.name, recorder.Code)
		}
	}

	// Delete then get.
	recorder = httptest.NewRecorder()
	fixture.app.handleDeleteContentDraft(recorder, draftRequest(t, http.MethodDelete, "/", outsider,
		map[string]string{"draftID": created.ID}, ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("outsider delete status = %d, want 404", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	fixture.app.handleDeleteContentDraft(recorder, draftRequest(t, http.MethodDelete, "/", fixture.userID,
		map[string]string{"draftID": created.ID}, ""))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("delete body = %q, want empty", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	fixture.app.handleGetContentDraft(recorder, draftRequest(t, http.MethodGet, "/", fixture.userID,
		map[string]string{"draftID": created.ID}, ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("get-after-delete status = %d, want 404", recorder.Code)
	}

	// Draft-scoped feature gate resolves the draft's workspace.
	features, err := featuresByContentDraftParam(fixture.app, draftRequest(t, http.MethodGet, "/", fixture.userID,
		map[string]string{"draftID": untitled.ID}, ""))
	if err != nil {
		t.Fatalf("draft feature resolve: %v", err)
	}
	if !features.AIChat {
		t.Fatalf("draft workspace ai_chat = false, want true")
	}
	if _, err := featuresByContentDraftParam(fixture.app, draftRequest(t, http.MethodGet, "/", outsider,
		map[string]string{"draftID": untitled.ID}, "")); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("outsider draft feature err = %v, want no rows", err)
	}
	if _, err := featuresByContentDraftParam(fixture.app, draftRequest(t, http.MethodGet, "/", fixture.userID, nil, "")); err == nil {
		t.Fatalf("missing draft param err = nil, want 400 param error")
	}
}

func mustParseUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse uuid: %v", err)
	}
	return id
}

func TestContentDraftTurnSubmission(t *testing.T) {
	fixture := newContentDraftFixture(t)
	ctx := fixture.ctx

	draft, err := fixture.queries.CreateContentDraftForUser(ctx, sqlc.CreateContentDraftForUserParams{
		UserID:    fixture.userID,
		ProjectID: fixture.projectID,
		Title:     pgtype.Text{String: "Turn draft", Valid: true},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	conversation, err := fixture.queries.CreateAIConversationForContentDraft(ctx, sqlc.CreateAIConversationForContentDraftParams{
		UserID:         fixture.userID,
		ProjectID:      fixture.projectID,
		Title:          "Turn draft",
		ContentDraftID: draft.ID,
	})
	if err != nil {
		t.Fatalf("create draft conversation: %v", err)
	}
	request, err := acceptAITurnRequest(aiTurnRequest{
		Content:         "Draft an intro",
		ReasoningEffort: "none",
		ClientRequestID: fmt.Sprintf("draft-turn-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("accept turn request: %v", err)
	}
	submission, err := fixture.app.submitAITurn(ctx, fixture.userID, conversation.ID, request)
	if err != nil {
		t.Fatalf("submit turn on draft conversation: %v", err)
	}
	if !submission.TurnID.Valid || !submission.UserMessageID.Valid || !submission.AssistantMessageID.Valid {
		t.Fatalf("submission = %+v", submission)
	}
}

func TestContentDraftRoutesRegistered(t *testing.T) {
	routes, ok := (&App{}).Router().(chi.Routes)
	if !ok {
		t.Fatalf("router is %T, want chi routes", (&App{}).Router())
	}
	router := routes
	want := map[string]string{
		"POST /projects/{projectID}/content/drafts": "create",
		"GET /projects/{projectID}/content/drafts":  "list",
		"GET /content/drafts/{draftID}":             "get",
		"PATCH /content/drafts/{draftID}":           "patch",
		"DELETE /content/drafts/{draftID}":          "delete",
	}
	seen := make(map[string]bool, len(want))
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if _, ok := want[method+" "+route]; ok {
			seen[method+" "+route] = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk router: %v", err)
	}
	for route := range want {
		if !seen[route] {
			t.Fatalf("route %s not registered", route)
		}
	}
}
