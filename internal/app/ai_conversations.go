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
)

type aiConversationResponse struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	LocationID      *string `json:"location_id"`
	CreatedByUserID string  `json:"created_by_user_id"`
	Title           string  `json:"title"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	TurnID          *string `json:"turn_id"`
	TurnStatus      *string `json:"turn_status"`
}

// createAIConversationRequest scopes a new conversation. A null or absent
// location_id creates a parent conversation; a location id binds it to that
// location of the project.
type createAIConversationRequest struct {
	LocationID *string `json:"location_id"`
}

type aiConversationDetailResponse struct {
	aiConversationResponse
	Messages []aiMessageResponse `json:"messages"`
}

// handleCreateAIConversation creates a parent or location conversation for a
// project member. A location id is verified against the project before insert,
// and the composite foreign key keeps the pair consistent.
func (a *App) handleCreateAIConversation(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body createAIConversationRequest
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	var locationID pgtype.UUID
	if body.LocationID != nil {
		trimmed := strings.TrimSpace(*body.LocationID)
		if trimmed == "" || locationID.Scan(trimmed) != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid location id")
			return
		}
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
	if locationID.Valid {
		if _, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{
			ID:     locationID,
			ID_2:   projectID,
			UserID: user.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "location not found")
			} else {
				serverError(w, r, err)
			}
			return
		}
	}
	conversation, err := a.Queries.CreateAIConversationForUser(r.Context(), sqlc.CreateAIConversationForUserParams{
		UserID:     user.ID,
		ProjectID:  projectID,
		LocationID: locationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAIConversationResponse(conversation, nil, nil))
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

	locationID, ok := conversationLocationFilter(w, r)
	if !ok {
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if locationID.Valid {
		if _, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{
			ID:     locationID,
			ID_2:   projectID,
			UserID: user.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "location not found")
				return
			}
			serverError(w, r, err)
			return
		}
	}
	total, err := a.Queries.CountAIConversationsForProjectForUser(r.Context(), sqlc.CountAIConversationsForProjectForUserParams{
		ProjectID:  projectID,
		UserID:     user.ID,
		LocationID: locationID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	conversations, err := a.Queries.ListAIConversationsForProjectForUser(r.Context(), sqlc.ListAIConversationsForProjectForUserParams{
		ProjectID:  projectID,
		UserID:     user.ID,
		LocationID: locationID,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	conversationIDs := make([]pgtype.UUID, 0, len(conversations))
	for _, conversation := range conversations {
		conversationIDs = append(conversationIDs, conversation.ID)
	}
	activeTurns, err := a.Queries.ListActiveTurnsForConversations(r.Context(), conversationIDs)
	if err != nil {
		serverError(w, r, err)
		return
	}

	activeTurnByConversation := make(map[string]struct{ id, status string }, len(activeTurns))
	for _, turn := range activeTurns {
		activeTurnByConversation[turn.ConversationID.String()] = struct{ id, status string }{
			id:     turn.TurnID.String(),
			status: turn.Status,
		}
	}

	responses := make([]aiConversationResponse, 0, len(conversations))
	for _, conversation := range conversations {
		var turnID, status *string
		if active, ok := activeTurnByConversation[conversation.ID.String()]; ok {
			active := active
			turnID = &active.id
			status = &active.status
		}
		responses = append(responses, newAIConversationResponse(aiConversationFromListRow(conversation), turnID, status))
	}

	setNoStore(w)
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
		conversation sqlc.GetAIConversationByIDForUserRow
		messages     []sqlc.ListAIMessagesForConversationRow
		turns        []sqlc.ListAITurnsForConversationRow
		toolCalls    []sqlc.ListAIToolCallsForConversationRow
	)
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		principal, ok := a.getPrincipal(w, r)

		if !ok {

			return errors.New("missing principal")

		}
		user := principal.User
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
	var turnID, turnStatus *string
	activeTurns, err := a.Queries.ListActiveTurnsForConversations(r.Context(), []pgtype.UUID{conversationID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if len(activeTurns) > 0 {
		turnIDValue := activeTurns[0].TurnID.String()
		turnID = &turnIDValue
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
		aiConversationResponse: newAIConversationResponse(aiConversationFromGetRow(conversation), turnID, turnStatus),
		Messages:               make([]aiMessageResponse, 0, len(messages)),
	}
	for _, message := range messages {
		item := aiMessageResponse{
			ID: message.ID.String(), Role: message.Role, Status: message.Status, Content: message.Content,
			Images:    imagesFromContentBlocks(message.Role, message.ContentBlocks),
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
		principal, ok := a.getPrincipal(w, r)

		if !ok {

			return errors.New("missing principal")

		}
		user := principal.User
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
	turnID *string,
	turnStatus *string,
) aiConversationResponse {
	response := aiConversationResponse{
		ID:              conversation.ID.String(),
		ProjectID:       conversation.ProjectID.String(),
		CreatedByUserID: conversation.CreatedByUserID.String(),
		Title:           conversation.Title,
		CreatedAt:       formatTimestamp(conversation.CreatedAt),
		UpdatedAt:       formatTimestamp(conversation.UpdatedAt),
		TurnID:          turnID,
		TurnStatus:      turnStatus,
	}
	if conversation.LocationID.Valid {
		value := conversation.LocationID.String()
		response.LocationID = &value
	}
	return response
}

// aiConversationFromListRow and aiConversationFromGetRow normalize the two
// generated row shapes to the AiConversation the response builder reads.
func aiConversationFromListRow(row sqlc.ListAIConversationsForProjectForUserRow) sqlc.AiConversation {
	return sqlc.AiConversation{
		ID: row.ID, ProjectID: row.ProjectID, CreatedByUserID: row.CreatedByUserID,
		Title: row.Title, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LocationID: row.LocationID,
	}
}

func aiConversationFromGetRow(row sqlc.GetAIConversationByIDForUserRow) sqlc.AiConversation {
	return sqlc.AiConversation{
		ID: row.ID, ProjectID: row.ProjectID, CreatedByUserID: row.CreatedByUserID,
		Title: row.Title, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LocationID: row.LocationID,
	}
}

// conversationLocationFilter reads the optional location_id query parameter.
// Absent or empty selects the parent scope (location_id NULL); a malformed
// value is a 400.
func conversationLocationFilter(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("location_id"))
	if raw == "" {
		return pgtype.UUID{}, true
	}
	var locationID pgtype.UUID
	if err := locationID.Scan(raw); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid location id")
		return pgtype.UUID{}, false
	}
	return locationID, true
}
