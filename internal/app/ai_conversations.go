package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiConversationResponse struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	CreatedByUserID string `json:"created_by_user_id"`
	Title           string `json:"title"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
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

	writeJSON(w, http.StatusCreated, newAIConversationResponse(conversation))
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
		return nil
	}) {
		return
	}

	responses := make([]aiConversationResponse, 0, len(conversations))
	for _, conversation := range conversations {
		responses = append(responses, newAIConversationResponse(conversation))
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
		messages     []sqlc.ListAIMessagesForConversationRow
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
		return nil
	}) {
		return
	}

	response := aiConversationDetailResponse{
		aiConversationResponse: newAIConversationResponse(conversation),
		Messages:               make([]aiMessageResponse, 0, len(messages)),
	}
	for _, message := range messages {
		response.Messages = append(response.Messages, aiMessageResponse{
			ID: message.ID.String(), Role: message.Role, Status: message.Status, Content: message.Content,
			CreatedAt: message.CreatedAt.Time, UpdatedAt: message.UpdatedAt.Time,
		})
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

func newAIConversationResponse(conversation sqlc.AiConversation) aiConversationResponse {
	return aiConversationResponse{
		ID:              conversation.ID.String(),
		ProjectID:       conversation.ProjectID.String(),
		CreatedByUserID: conversation.CreatedByUserID.String(),
		Title:           conversation.Title,
		CreatedAt:       formatTimestamp(conversation.CreatedAt),
		UpdatedAt:       formatTimestamp(conversation.UpdatedAt),
	}
}
