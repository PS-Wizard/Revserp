package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
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
	defer tx.Rollback(r.Context())

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
	defer tx.Rollback(r.Context())

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
	defer tx.Rollback(r.Context())

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
	defer tx.Rollback(r.Context())

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
	var requestBody createAIConversationMessageRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	requestBody.PillarID = strings.TrimSpace(requestBody.PillarID)
	requestBody.BucketID = strings.TrimSpace(requestBody.BucketID)
	requestBody.BucketIDs = normalizeStringIDs(requestBody.BucketIDs)
	if len(requestBody.BucketIDs) == 0 && requestBody.BucketID != "" {
		requestBody.BucketIDs = []string{requestBody.BucketID}
	}
	requestBody.IssueTypeIDs = normalizeStringIDs(requestBody.IssueTypeIDs)
	requestBody.Content = strings.TrimSpace(requestBody.Content)
	if requestBody.PillarID == "" || len(requestBody.BucketIDs) == 0 || requestBody.Content == "" {
		writeJSONError(w, http.StatusBadRequest, "pillar_id, bucket_ids, and content are required")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

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

	crawlID, err := resolveAIMessageCrawlID(requestBody.CrawlID, conversation.CrawlID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
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
	if !sameUUID(crawl.ProjectID, conversation.ProjectID) {
		writeJSONError(w, http.StatusBadRequest, "crawl does not belong to conversation project")
		return
	}

	breakdownRow, err := queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl score breakdown not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	var snapshot issueshared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(breakdownRow.BreakdownJson, &snapshot); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	fixRequest := aiFixRequest{
		PillarID:     requestBody.PillarID,
		BucketIDs:    requestBody.BucketIDs,
		IssueTypeIDs: requestBody.IssueTypeIDs,
	}
	pillar, buckets, selectedIssues, err := resolveAIFixScope(snapshot, fixRequest)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	issueRows, err := loadAIFixIssueRows(r, tx, crawlID, user.ID, requestBody.PillarID, requestBody.BucketIDs, requestBody.IssueTypeIDs)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	businessProfile, hasBusinessProfile, err := getProjectBusinessProfileByProjectID(r.Context(), queries, crawl.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	previousMessages, err := queries.ListAIMessagesForConversationForUser(r.Context(), sqlc.ListAIMessagesForConversationForUserParams{ConversationID: conversationID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	promptMessages := append(aiFixMessagesFromRows(previousMessages), aiFixMessage{Role: "user", Content: requestBody.Content})
	prompt := buildAIFixPrompt(pillar, buckets, selectedIssues, issueRows, businessProfile, hasBusinessProfile, normalizeAIFixMessages(promptMessages))
	content, model, err := a.generateAIText(r.Context(), prompt)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	scopePayload, err := json.Marshal(newAIMessageScope(pillar, buckets, selectedIssues))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	persistTx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer persistTx.Rollback(r.Context())
	persistQueries := a.Queries.WithTx(persistTx)
	userMessage, err := persistQueries.CreateAIMessage(r.Context(), sqlc.CreateAIMessageParams{
		ConversationID: conversationID,
		Role:           "user",
		Content:        requestBody.Content,
		CrawlID:        crawlID,
		Scope:          scopePayload,
		Model:          pgtype.Text{},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	assistantMessage, err := persistQueries.CreateAIMessage(r.Context(), sqlc.CreateAIMessageParams{
		ConversationID: conversationID,
		Role:           "assistant",
		Content:        content,
		CrawlID:        crawlID,
		Scope:          scopePayload,
		Model:          aiNullableText(model),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	updatedConversation, err := persistQueries.UpdateAIConversationTouched(r.Context(), conversationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := persistTx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, createAIConversationMessageResponse{
		Conversation:     newAIConversationResponse(updatedConversation, int32(len(previousMessages)+2)),
		UserMessage:      newAIMessageResponse(userMessage),
		AssistantMessage: newAIMessageResponse(assistantMessage),
		Scope: aiFixScopeInfo{
			PillarLabel: pillar.Label,
			BucketLabel: aiFixBucketLabel(buckets),
			IssueCount:  len(selectedIssues),
			URLCount:    aiFixBucketURLCount(buckets),
		},
	})
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

func newAIConversationResponsesFromProjectRows(rows []sqlc.ListAIConversationsForProjectForUserRow) []aiConversationResponse {
	responses := make([]aiConversationResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, aiConversationResponse{
			ID:              row.ID.String(),
			ProjectID:       row.ProjectID.String(),
			CrawlID:         optionalUUIDString(row.CrawlID),
			CreatedByUserID: row.CreatedByUserID.String(),
			Title:           nullableTextString(row.Title),
			MessageCount:    row.MessageCount,
			CreatedAt:       formatTimestamp(row.CreatedAt),
			UpdatedAt:       formatTimestamp(row.UpdatedAt),
		})
	}
	return responses
}

func newAIConversationResponsesFromCrawlRows(rows []sqlc.ListAIConversationsForCrawlForUserRow) []aiConversationResponse {
	responses := make([]aiConversationResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, aiConversationResponse{
			ID:              row.ID.String(),
			ProjectID:       row.ProjectID.String(),
			CrawlID:         optionalUUIDString(row.CrawlID),
			CreatedByUserID: row.CreatedByUserID.String(),
			Title:           nullableTextString(row.Title),
			MessageCount:    row.MessageCount,
			CreatedAt:       formatTimestamp(row.CreatedAt),
			UpdatedAt:       formatTimestamp(row.UpdatedAt),
		})
	}
	return responses
}

func newAIConversationResponse(conversation sqlc.AiConversation, messageCount int32) aiConversationResponse {
	return aiConversationResponse{
		ID:              conversation.ID.String(),
		ProjectID:       conversation.ProjectID.String(),
		CrawlID:         optionalUUIDString(conversation.CrawlID),
		CreatedByUserID: conversation.CreatedByUserID.String(),
		Title:           nullableTextString(conversation.Title),
		MessageCount:    messageCount,
		CreatedAt:       formatTimestamp(conversation.CreatedAt),
		UpdatedAt:       formatTimestamp(conversation.UpdatedAt),
	}
}

func newAIMessageResponses(messages []sqlc.AiMessage) []aiMessageResponse {
	responses := make([]aiMessageResponse, 0, len(messages))
	for _, message := range messages {
		responses = append(responses, newAIMessageResponse(message))
	}
	return responses
}

func newAIMessageResponse(message sqlc.AiMessage) aiMessageResponse {
	return aiMessageResponse{
		ID:             message.ID.String(),
		ConversationID: message.ConversationID.String(),
		Role:           message.Role,
		Content:        message.Content,
		CrawlID:        optionalUUIDString(message.CrawlID),
		Scope:          json.RawMessage(message.Scope),
		Model:          nullableTextString(message.Model),
		CreatedAt:      formatTimestamp(message.CreatedAt),
	}
}

func aiFixMessagesFromRows(messages []sqlc.AiMessage) []aiFixMessage {
	responses := make([]aiFixMessage, 0, len(messages))
	for _, message := range messages {
		responses = append(responses, aiFixMessage{Role: message.Role, Content: message.Content})
	}
	return responses
}

func newAIMessageScope(pillar issueshared.PillarScoreBreakdown, buckets []issueshared.BucketScoreBreakdown, selectedIssues []issueshared.IssueTypeScoreBreakdown) aiMessageScope {
	scope := aiMessageScope{
		PillarID:        pillar.ID,
		PillarLabel:     pillar.Label,
		BucketIDs:       make([]string, 0, len(buckets)),
		BucketLabels:    make([]string, 0, len(buckets)),
		IssueTypeIDs:    make([]string, 0, len(selectedIssues)),
		IssueTypeLabels: make([]string, 0, len(selectedIssues)),
	}
	for _, bucket := range buckets {
		scope.BucketIDs = append(scope.BucketIDs, bucket.ID)
		scope.BucketLabels = append(scope.BucketLabels, bucket.Label)
	}
	for _, issue := range selectedIssues {
		scope.IssueTypeIDs = append(scope.IssueTypeIDs, issue.ID)
		scope.IssueTypeLabels = append(scope.IssueTypeLabels, issue.Label)
	}
	return scope
}

func aiNullableText(value string) pgtype.Text {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: trimmedValue, Valid: true}
}

func nullableTextString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func optionalUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func sameUUID(left pgtype.UUID, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.String() == right.String()
}
