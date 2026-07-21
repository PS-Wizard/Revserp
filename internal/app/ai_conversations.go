package app

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/app/aitools"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const defaultAIConversationLimit = 50

type createAIConversationRequest struct {
	Title string `json:"title"`
}

// createAIConversationMessageRequest carries the new user turn plus the
// current dashboard context (project/crawl) from the frontend URL state.
// project_id/crawl_id MAY be empty; the model never supplies tenant IDs.
type createAIConversationMessageRequest struct {
	Content   string `json:"content"`
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id"`
	Timezone  string `json:"timezone"`
}

type aiConversationDetailResponse struct {
	Conversation aiConversationResponse `json:"conversation"`
	Messages     []aiMessageResponse    `json:"messages"`
}

// agentTurnContext holds all data needed to seed one agent turn: ownership
// checks are folded into loading it (each query is a "...ForUser" query). The
// project/crawl are optional dashboard context injected per message.
type agentTurnContext struct {
	projectID          pgtype.UUID
	crawlID            pgtype.UUID
	hasProject         bool
	hasCrawl           bool
	project            sqlc.Project
	crawl              sqlc.GetCrawlByIDForUserRow
	businessProfile    sqlc.GetProjectBusinessProfileByProjectIDRow
	hasBusinessProfile bool
	previousMessages   []sqlc.ListAIMessagesForConversationForUserRow
}

// activeOrgFromRequest returns the caller's active organization id, or an
// error message when no organization is active on the session.
func activeOrgFromRequest(r *http.Request) (pgtype.UUID, bool) {
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok || !session.ActiveOrgID.Valid {
		return pgtype.UUID{}, false
	}
	return session.ActiveOrgID, true
}

// handleListAIConversations lists the active organization's chat history.
func (a *App) handleListAIConversations(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if limit == defaultPaginationLimit {
		limit = defaultAIConversationLimit
	}
	orgID, ok := activeOrgFromRequest(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "no active organization")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, err := queries.ListAIConversationsForOrgForUser(r.Context(), sqlc.ListAIConversationsForOrgForUserParams{
		OrgID:  orgID,
		UserID: user.ID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": newAIConversationResponsesFromOrgRows(rows)})
}

// handleCreateAIConversation creates an empty org-scoped chat thread.
func (a *App) handleCreateAIConversation(w http.ResponseWriter, r *http.Request) {
	var requestBody createAIConversationRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	orgID, ok := activeOrgFromRequest(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "no active organization")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	conversation, err := queries.CreateAIConversation(r.Context(), sqlc.CreateAIConversationParams{
		OrgID:           orgID,
		CreatedByUserID: user.ID,
		Title:           aiNullableText(truncateAIFixText(strings.TrimSpace(requestBody.Title), 120)),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"conversation": newAIConversationResponse(conversation.ID, conversation.OrgID, conversation.CreatedByUserID, conversation.Title, conversation.CreatedAt, conversation.UpdatedAt, 0),
	})
}

// handleGetAIConversation returns one chat thread and its messages.
func (a *App) handleGetAIConversation(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	conversation, err := queries.GetAIConversationForUser(r.Context(), sqlc.GetAIConversationForUserParams{ID: conversationID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	messages, err := queries.ListAIMessagesForConversationForUser(r.Context(), sqlc.ListAIMessagesForConversationForUserParams{ConversationID: conversationID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, aiConversationDetailResponse{
		Conversation: newAIConversationResponse(conversation.ID, conversation.OrgID, conversation.CreatedByUserID, conversation.Title, conversation.CreatedAt, conversation.UpdatedAt, int32(len(messages))),
		Messages:     newAIMessageResponses(messages),
	})
}

// handleDeleteAIConversation deletes one chat thread and its messages.
func (a *App) handleDeleteAIConversation(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if _, err := queries.DeleteAIConversationForUser(r.Context(), sqlc.DeleteAIConversationForUserParams{ID: conversationID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCreateAIConversationMessage runs one agentic chat turn over SSE:
// it persists the user message, then streams the model's reasoning, text,
// and tool calls to the client as they happen, persisting every assistant
// and tool message row along the way.
func (a *App) handleCreateAIConversationMessage(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	var req createAIConversationMessageRequest
	if err := readJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		writeJSONError(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > maxAIChatMessageBytes {
		writeJSONError(w, http.StatusBadRequest, "content is too large")
		return
	}

	orgID, ok := activeOrgFromRequest(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "no active organization")
		return
	}
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Serialize all turns for this conversation with a session-level
	// pg_try_advisory_lock held on a dedicated connection for the duration of
	// the request. Unlike pg_advisory_lock, this never blocks: a concurrent
	// turn on the same conversation gets an immediate 409 instead of queuing
	// behind a slow LLM call. The lock connection is never used for queries
	// during the turn; those go through the normal pool.
	lockConn, err := a.DB.Acquire(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer lockConn.Release()

	turnLockKey := conversationAdvisoryLockKey(conversationID)
	var acquired bool
	if err := lockConn.QueryRow(r.Context(), "select pg_try_advisory_lock($1)", turnLockKey).Scan(&acquired); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !acquired {
		writeJSONError(w, http.StatusConflict, "a turn is already in progress")
		return
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = lockConn.Exec(unlockCtx, "select pg_advisory_unlock($1)", turnLockKey)
	}()

	turnCtx, httpStatus, err := a.loadAgentTurnContext(r.Context(), a.Queries, conversationID, user.ID, req.ProjectID, req.CrawlID)
	if err != nil {
		if httpStatus > 0 {
			writeJSONError(w, httpStatus, err.Error())
		} else {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	systemPrompt := loadEffectiveAgentSystemPrompt(r.Context(), a.Queries) + buildAgentContextBlock(turnCtx)
	if _, err := boundedAgentMessages(systemPrompt, cappedReplayGroups(replayMessageGroups(turnCtx.previousMessages)), req.Content, nil, a.AIToolRegistry.Defs()); err != nil {
		writeJSONError(w, http.StatusBadRequest, errAIRequestTooLarge.Error())
		return
	}

	// Persist the user message before starting the stream so it survives a
	// client refresh even if the turn that follows fails or is abandoned.
	if _, err := a.Queries.CreateAIMessage(r.Context(), sqlc.CreateAIMessageParams{
		ConversationID: conversationID,
		Role:           "user",
		Content:        req.Content,
		CrawlID:        turnCtx.crawlID,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if _, err := a.Queries.UpdateAIConversationTouched(r.Context(), conversationID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	scope := aitools.Scope{
		UserID:    user.ID,
		OrgID:     orgID,
		ProjectID: turnCtx.projectID,
		CrawlID:   turnCtx.crawlID,
		Timezone:  sanitizeRequestTimezone(req.Timezone),
		Queries:   a.Queries,
	}

	sse := newSSEWriter(w)

	// The turn deadline bounds total provider and tool time while the SSE writer
	// keeps its independent sliding write deadline for active streams.
	turnTimeout := a.Config.AITurnTimeout
	if turnTimeout <= 0 {
		turnTimeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), turnTimeout)
	defer cancel()

	_ = runAgentTurn(ctx, agentTurnParams{
		Client:         a.AIClient,
		Registry:       a.AIToolRegistry,
		Queries:        a.Queries,
		ConversationID: conversationID,
		Scope:          scope,
		SystemPrompt:   systemPrompt,
		History:        turnCtx.previousMessages,
		UserContent:    req.Content,
		SSE:            sse,
		MaxTokens:      agentMaxTokens,
	})
}

// loadAgentTurnContext fetches everything needed to seed one agent turn.
// The conversation is org-scoped; project/crawl are optional dashboard context
// supplied in the message body and validated for ownership here. On a client
// error (400/404) it returns a non-zero httpStatus with the message in err.
// On a server error it returns httpStatus == 0 with err populated.
func (a *App) loadAgentTurnContext(ctx context.Context, queries *sqlc.Queries, conversationID pgtype.UUID, userID pgtype.UUID, rawProjectID, rawCrawlID string) (*agentTurnContext, int, error) {
	if _, err := queries.GetAIConversationForUser(ctx, sqlc.GetAIConversationForUserParams{ID: conversationID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, http.StatusNotFound, errors.New("conversation not found")
		}
		return nil, 0, err
	}

	turnCtx := &agentTurnContext{}

	trimmedProjectID := strings.TrimSpace(rawProjectID)
	if trimmedProjectID != "" {
		projectID, err := parseUUIDParam(trimmedProjectID)
		if err != nil {
			return nil, http.StatusBadRequest, errors.New("invalid project id")
		}
		project, err := queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, http.StatusNotFound, errors.New("project not found")
			}
			return nil, 0, err
		}
		turnCtx.projectID = projectID
		turnCtx.hasProject = true
		turnCtx.project = project

		businessProfile, hasBusinessProfile, err := getProjectBusinessProfileByProjectID(ctx, queries, projectID)
		if err != nil {
			return nil, 0, err
		}
		turnCtx.businessProfile = businessProfile
		turnCtx.hasBusinessProfile = hasBusinessProfile
	}

	trimmedCrawlID := strings.TrimSpace(rawCrawlID)
	if trimmedCrawlID != "" {
		crawlID, err := parseUUIDParam(trimmedCrawlID)
		if err != nil {
			return nil, http.StatusBadRequest, errors.New("invalid crawl id")
		}
		crawl, err := queries.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, http.StatusNotFound, errors.New("crawl not found")
			}
			return nil, 0, err
		}
		if turnCtx.hasProject && !sameUUID(crawl.ProjectID, turnCtx.projectID) {
			return nil, http.StatusBadRequest, errors.New("crawl does not belong to project")
		}
		turnCtx.crawlID = crawlID
		turnCtx.hasCrawl = true
		turnCtx.crawl = crawl
	}

	previousMessages, err := queries.ListAIMessagesForConversationForUser(ctx, sqlc.ListAIMessagesForConversationForUserParams{ConversationID: conversationID, UserID: userID})
	if err != nil {
		return nil, 0, err
	}
	turnCtx.previousMessages = previousMessages

	return turnCtx, 0, nil
}

// buildAgentContextBlock renders the cheap ambient context (project, business
// profile, current crawl and scores) appended after the system prompt so the
// model never has to ask which project/crawl it is operating on. Project and
// crawl are optional; the block only includes whatever context is present.
func buildAgentContextBlock(t *agentTurnContext) string {
	if !t.hasProject && !t.hasCrawl {
		return "\n\n## Current context\n- No project or crawl is currently open. Use list_projects and switch_project to pick one, or ask the user to open a project.\n"
	}
	var b strings.Builder
	b.WriteString("\n\n## Current context\n")
	if t.hasProject {
		fmt.Fprintf(&b, "- Project: %s (%s)\n", t.project.Name, t.project.BaseUrl)
		if t.hasBusinessProfile {
			b.WriteString("- Business: ")
			b.WriteString(t.businessProfile.BrandName)
			if category := aiFixTextValue(t.businessProfile.PrimaryCategory); category != "" {
				b.WriteString(", ")
				b.WriteString(category)
			}
			if location := aiFixTextValue(t.businessProfile.PrimaryLocation); location != "" {
				b.WriteString(", ")
				b.WriteString(location)
			}
			b.WriteString("\n")
		}
	}
	if t.hasCrawl {
		fmt.Fprintf(&b, "- Crawl: %s, crawled %s\n", t.crawlID.String(), formatTimestamp(t.crawl.CreatedAt))
		fmt.Fprintf(&b, "- Scores: overall %d, SEO %d, AEO %d, PageSpeed %d\n",
			t.crawl.OverallScore.Int32, t.crawl.SeoScore.Int32, t.crawl.AeoScore.Int32, t.crawl.PagespeedScore.Int32)
	}
	return b.String()
}

// sanitizeRequestTimezone trims and validates a client-supplied IANA timezone,
// returning "" when it is empty or not a loadable location. It becomes the
// default timezone for configure_auto_crawl when the model omits one.
func sanitizeRequestTimezone(raw string) string {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ""
	}
	return tz
}

// conversationAdvisoryLockKey derives a stable int8 key from a conversation
// UUID for use with pg_try_advisory_lock/pg_advisory_unlock.
func conversationAdvisoryLockKey(conversationID pgtype.UUID) int64 {
	return int64(binary.BigEndian.Uint64(conversationID.Bytes[8:16]))
}
