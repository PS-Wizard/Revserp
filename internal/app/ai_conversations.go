package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiConversationResponse struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	CreatedByUserID string  `json:"created_by_user_id"`
	Title           string  `json:"title"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	TurnStatus      *string `json:"turn_status"`
}

type aiConversationDetailResponse struct {
	aiConversationResponse
	Messages []aiMessageResponse `json:"messages"`
}

// handleCreateAIConversation creates a conversation for a project member.
func (a *App) handleCreateAIConversation(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var conversation sqlc.AiConversation
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		conversation, err = queries.CreateAIConversationForUser(r.Context(), sqlc.CreateAIConversationForUserParams{
			ProjectID: projectID,
			UserID:    user.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "project not found")
				return err
			}
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusCreated, newAIConversationResponse(conversation, nil))
}

// handleListAIConversations lists a project's conversations for a member.
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

	var (
		conversations []sqlc.AiConversation
		total         int64
		activeTurns   []sqlc.ListActiveTurnsForConversationsRow
	)
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
			ID:     projectID,
			UserID: user.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "project not found")
				return err
			}
			serverError(w, r, err)
			return err
		}

		total, err = queries.CountAIConversationsForProjectForUser(r.Context(), sqlc.CountAIConversationsForProjectForUserParams{
			ProjectID: projectID,
			UserID:    user.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		conversations, err = queries.ListAIConversationsForProjectForUser(r.Context(), sqlc.ListAIConversationsForProjectForUserParams{
			ProjectID:  projectID,
			UserID:     user.ID,
			PageLimit:  limit,
			PageOffset: offset,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}

		activeTurns, err = queries.ListActiveTurnsForConversations(r.Context(), conversationUUIDs(conversations))
		if err != nil {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	activeStatusByConversation := make(map[string]string, len(activeTurns))
	for _, turn := range activeTurns {
		activeStatusByConversation[turn.ConversationID.String()] = turn.Status
	}

	responses := make([]aiConversationResponse, 0, len(conversations))
	for _, conversation := range conversations {
		var status *string
		if value, ok := activeStatusByConversation[conversation.ID.String()]; ok {
			status = &value
		}
		responses = append(responses, newAIConversationResponse(conversation, status))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleGetAIConversation returns a conversation for a member.
func (a *App) handleGetAIConversation(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	var (
		conversation sqlc.AiConversation
		messages     []sqlc.AiMessage
		turns        []sqlc.ListAITurnsForConversationRow
		toolCalls    []sqlc.ListAIToolCallsForConversationRow
	)
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}
		conversation, err = queries.GetAIConversationByIDForUser(r.Context(), sqlc.GetAIConversationByIDForUserParams{
			ConversationID: conversationID,
			UserID:         user.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "conversation not found")
				return err
			}
			serverError(w, r, err)
			return err
		}
		messages, err = queries.ListAIMessagesForConversation(r.Context(), conversationID)
		if err != nil {
			serverError(w, r, err)
			return err
		}
		turns, err = queries.ListAITurnsForConversation(r.Context(), conversationID)
		if err != nil {
			serverError(w, r, err)
			return err
		}
		toolCalls, err = queries.ListAIToolCallsForConversation(r.Context(), conversationID)
		if err != nil {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}
	var turnStatus *string
	activeTurns, err := a.Queries.ListActiveTurnsForConversations(r.Context(), conversationUUIDs([]sqlc.AiConversation{{ID: conversationID}}))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if len(activeTurns) > 0 {
		turnStatus = &activeTurns[0].Status
	}

	// Activity per turn: tool calls plus the turn's run window, replayed onto
	// each assistant message so a reopened conversation keeps the live tool UI.
	type turnActivity struct {
		startedAt   pgtype.Timestamptz
		completedAt pgtype.Timestamptz
		toolCalls   []aiToolCallResponse
	}
	activityByTurn := make(map[pgtype.UUID]turnActivity, len(turns))
	for _, turn := range turns {
		activityByTurn[turn.ID] = turnActivity{startedAt: turn.StartedAt, completedAt: turn.CompletedAt}
	}
	for _, call := range toolCalls {
		activity := activityByTurn[call.TurnID]
		activity.toolCalls = append(activity.toolCalls, aiToolCallResponse{
			CallID: call.CallID, Name: call.Name, Args: json.RawMessage(call.Args),
			Status: call.Status, Summary: call.Summary, Seq: call.Seq, CreatedAt: call.CreatedAt.Time,
		})
		activityByTurn[call.TurnID] = activity
	}

	response := aiConversationDetailResponse{
		aiConversationResponse: newAIConversationResponse(conversation, turnStatus),
		Messages:               make([]aiMessageResponse, 0, len(messages)),
	}
	for _, message := range messages {
		item := aiMessageResponse{
			ID: message.ID.String(), Role: message.Role, Status: message.Status, Content: message.Content,
			CreatedAt: message.CreatedAt.Time, UpdatedAt: message.UpdatedAt.Time,
		}
		if message.Role == "assistant" {
			if activity, ok := activityByTurn[message.TurnID]; ok {
				if len(activity.toolCalls) > 0 {
					item.ToolCalls = activity.toolCalls
				}
				if activity.startedAt.Valid {
					startedAt := activity.startedAt.Time
					item.ActivityStartedAt = &startedAt
				}
				if activity.completedAt.Valid {
					completedAt := activity.completedAt.Time
					item.ActivityEndedAt = &completedAt
				}
			}
		}
		response.Messages = append(response.Messages, item)
	}
	writeJSON(w, http.StatusOK, response)
}

// handleDeleteAIConversation hard-deletes a conversation for a member.
func (a *App) handleDeleteAIConversation(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}
		deletedRows, err := queries.DeleteAIConversationByIDForUser(r.Context(), sqlc.DeleteAIConversationByIDForUserParams{
			ConversationID: conversationID,
			UserID:         user.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		if deletedRows == 0 {
			writeJSONError(w, http.StatusNotFound, "conversation not found")
			return errors.New("conversation not found")
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func newAIConversationResponse(
	conversation sqlc.AiConversation,
	turnStatus *string,
) aiConversationResponse {
	return aiConversationResponse{
		ID:              conversation.ID.String(),
		ProjectID:       conversation.ProjectID.String(),
		CreatedByUserID: conversation.CreatedByUserID.String(),
		Title:           conversation.Title,
		CreatedAt:       formatTimestamp(conversation.CreatedAt),
		UpdatedAt:       formatTimestamp(conversation.UpdatedAt),
		TurnStatus:      turnStatus,
	}
}

func conversationUUIDs(conversations []sqlc.AiConversation) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, len(conversations))
	for _, conversation := range conversations {
		ids = append(ids, conversation.ID)
	}
	return ids
}
