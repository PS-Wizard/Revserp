package app

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const defaultAIConversationLimit = 50

type createAIConversationRequest struct {
	CrawlID string `json:"crawl_id"`
	Title   string `json:"title"`
}

type createAIConversationMessageRequest struct {
	CrawlID      string   `json:"crawl_id"`
	PillarID     string   `json:"pillar_id"`
	BucketID     string   `json:"bucket_id"`
	BucketIDs    []string `json:"bucket_ids"`
	IssueTypeIDs []string `json:"issue_type_ids"`
	IssueURLs    []string `json:"issue_urls"`
	Content      string   `json:"content"`
}

type aiConversationResponse struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	CrawlID         string `json:"crawl_id,omitempty"`
	CreatedByUserID string `json:"created_by_user_id"`
	Title           string `json:"title,omitempty"`
	MessageCount    int32  `json:"message_count"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type aiMessageResponse struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	CrawlID        string          `json:"crawl_id,omitempty"`
	Scope          json.RawMessage `json:"scope,omitempty"`
	Model          string          `json:"model,omitempty"`
	CreatedAt      string          `json:"created_at"`
}

type aiConversationDetailResponse struct {
	Conversation aiConversationResponse `json:"conversation"`
	Messages     []aiMessageResponse    `json:"messages"`
}

type createAIConversationMessageResponse struct {
	Conversation     aiConversationResponse `json:"conversation"`
	UserMessage      aiMessageResponse      `json:"user_message"`
	AssistantMessage aiMessageResponse      `json:"assistant_message"`
	Scope            aiFixScopeInfo         `json:"scope"`
}

type aiMessageScope struct {
	PillarID        string   `json:"pillar_id"`
	PillarLabel     string   `json:"pillar_label"`
	BucketIDs       []string `json:"bucket_ids"`
	BucketLabels    []string `json:"bucket_labels"`
	IssueTypeIDs    []string `json:"issue_type_ids"`
	IssueTypeLabels []string `json:"issue_type_labels"`
	IssueURLs       []string `json:"issue_urls,omitempty"`
}

// handleListAIConversations lists project chat history for the current user.
func (a *App) handleListAIConversations(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if limit == defaultPaginationLimit {
		limit = defaultAIConversationLimit
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
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawlIDValue := strings.TrimSpace(r.URL.Query().Get("crawl_id"))
	if crawlIDValue != "" {
		crawlID, err := parseUUIDParam(crawlIDValue)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
			return
		}
		crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if !sameUUID(crawl.ProjectID, projectID) {
			writeJSONError(w, http.StatusBadRequest, "crawl does not belong to project")
			return
		}
		rows, err := queries.ListAIConversationsForCrawlForUser(r.Context(), sqlc.ListAIConversationsForCrawlForUserParams{
			ProjectID: projectID,
			CrawlID:   crawlID,
			UserID:    user.ID,
			Limit:     limit,
			Offset:    offset,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": newAIConversationResponsesFromCrawlRows(rows)})
		return
	}

	rows, err := queries.ListAIConversationsForProjectForUser(r.Context(), sqlc.ListAIConversationsForProjectForUserParams{
		ProjectID: projectID,
		UserID:    user.ID,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": newAIConversationResponsesFromProjectRows(rows)})
}

// handleCreateAIConversation creates an empty project chat thread.
func (a *App) handleCreateAIConversation(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var requestBody createAIConversationRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
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
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawlID := pgtype.UUID{}
	if strings.TrimSpace(requestBody.CrawlID) != "" {
		parsedCrawlID, err := parseUUIDParam(requestBody.CrawlID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
			return
		}
		crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: parsedCrawlID, UserID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if !sameUUID(crawl.ProjectID, projectID) {
			writeJSONError(w, http.StatusBadRequest, "crawl does not belong to project")
			return
		}
		crawlID = parsedCrawlID
	}

	conversation, err := queries.CreateAIConversation(r.Context(), sqlc.CreateAIConversationParams{
		ProjectID:       projectID,
		CrawlID:         crawlID,
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
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": newAIConversationResponse(conversation, 0)})
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
		Conversation: newAIConversationResponse(conversation, int32(len(messages))),
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

// handleCreateAIConversationMessage answers and persists one scoped chat turn.
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
	if errMsg := normalizeMessageRequest(&req); errMsg != "" {
		writeJSONError(w, http.StatusBadRequest, errMsg)
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

	turnCtx, httpStatus, err := a.loadConversationTurnContext(r, queries, tx, conversationID, user.ID, &req)
	if err != nil {
		if httpStatus > 0 {
			writeJSONError(w, httpStatus, err.Error())
		} else {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	// Truncate the user message to the configured maximum before building the prompt.
	userContent := truncateAIFixText(req.Content, maxAIFixMessageLength)

	systemPrompt := loadEffectiveAISystemPrompt(r.Context(), a.Queries)
	prompt := buildTurnPrompt(systemPrompt, turnCtx, userContent)
	content, model, err := a.generateAIText(r.Context(), prompt)
	if err != nil {
		log.Printf("AI provider error (handleCreateAIConversationMessage): %v", err)
		writeJSONError(w, http.StatusBadGateway, "AI provider unavailable")
		return
	}

	scopePayload, err := marshalScopePayload(turnCtx.pillar, turnCtx.buckets, turnCtx.selectedIssues, req.IssueURLs)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	userMessage, assistantMessage, updatedConversation, err := persistTurnMessages(r.Context(), queries, conversationID, turnCtx.crawlID, userContent, scopePayload, content, model)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newCreateMessageResponse(updatedConversation, userMessage, assistantMessage, turnCtx.previousMessages, turnCtx.pillar, turnCtx.buckets, turnCtx.selectedIssues))
}

func resolveAIMessageCrawlID(rawCrawlID string, fallback pgtype.UUID) (pgtype.UUID, error) {
	trimmedCrawlID := strings.TrimSpace(rawCrawlID)
	if trimmedCrawlID != "" {
		return parseUUIDParam(trimmedCrawlID)
	}
	if fallback.Valid {
		return fallback, nil
	}
	return pgtype.UUID{}, errors.New("crawl_id is required")
}
