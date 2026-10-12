package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// answerUserQuestionBody resolves one parked set; strict-decoded.
type answerUserQuestionBody struct {
	Decision string                                `json:"decision"`
	Answers  []aichattools.UserQuestionAnswerInput `json:"answers,omitempty"`
	Reply    string                                `json:"reply,omitempty"`
}

// userQuestionRequestJSON renders one row in the contract entity shape.
func userQuestionRequestJSON(row sqlc.ListUserQuestionRequestsForConversationRow) (map[string]any, error) {
	return userQuestionRequestJSONRaw(row.ID.String(), row.TurnID.String(), row.ToolCallID, row.Status, row.Questions, row.Answers, row.Reply, row.CreatedAt.Time, nullableTime(row.AnsweredAt))
}

func userQuestionRequestJSONRaw(id, turnID, toolCallID, status string, questions, answers []byte, reply string, createdAt time.Time, answeredAt *time.Time) (map[string]any, error) {
	encoded, err := aichattools.MarshalUserQuestionRequest(id, turnID, toolCallID, status, questions, answers, reply, createdAt, answeredAt)
	if err != nil {
		return nil, err
	}
	var entity map[string]any
	if err := json.Unmarshal(encoded, &entity); err != nil {
		return nil, err
	}
	return entity, nil
}

func (a *App) handleListUserQuestionRequests(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	if _, err := a.Queries.GetAIConversationByIDForUser(r.Context(), sqlc.GetAIConversationByIDForUserParams{
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "conversation not found")
			return
		}
		serverError(w, r, err)
		return
	}
	rows, err := a.Queries.ListUserQuestionRequestsForConversation(r.Context(), conversationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	requests := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entity, err := userQuestionRequestJSON(row)
		if err != nil {
			serverError(w, r, err)
			return
		}
		requests = append(requests, entity)
	}
	writeJSON(w, http.StatusOK, map[string]any{"question_requests": requests})
}

// handleAnswerUserQuestionRequest resolves one set and requeues the SAME
// turn; identical repeats replay, anything else conflicts.
func (a *App) handleAnswerUserQuestionRequest(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	requestID, err := parseUUIDParam(chi.URLParam(r, "questionRequestID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid question request id")
		return
	}
	var body answerUserQuestionBody
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)

	conversation, err := queries.LockAIConversationForTurn(r.Context(), sqlc.LockAIConversationForTurnParams{
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "conversation not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if !conversation.AiChat {
		writeJSONError(w, http.StatusForbidden, "ai_chat_disabled")
		return
	}
	pre, err := queries.GetUserQuestionRequestForDecision(r.Context(), sqlc.GetUserQuestionRequestForDecisionParams{
		ID:             requestID,
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "question request not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if pre.CreatedByUserID != principal.User.ID {
		writeJSONError(w, http.StatusForbidden, "only the turn initiator can answer")
		return
	}
	if pre.Status != "pending" {
		a.replayUserQuestionDecision(w, r, tx, requestID, pre.Questions, body)
		return
	}
	status, canonicalAnswers, canonicalReply, err := aichattools.ValidateUserQuestionDecision(pre.Questions, body.Decision, body.Answers, body.Reply)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	turn, err := queries.LockAITurnForUser(r.Context(), sqlc.LockAITurnForUserParams{
		UserID: principal.User.ID,
		TurnID: pre.TurnID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "turn not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if turn.ConversationID != conversationID {
		writeJSONError(w, http.StatusNotFound, "question request not found")
		return
	}
	locked, err := queries.LockUserQuestionRequestForDecision(r.Context(), sqlc.LockUserQuestionRequestForDecisionParams{
		ID:             requestID,
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "question request already decided")
			return
		}
		serverError(w, r, err)
		return
	}
	if locked.Status != "pending" {
		a.replayUserQuestionDecision(w, r, tx, requestID, locked.Questions, body)
		return
	}
	if turn.Status != "waiting_for_user" || locked.TurnStatus != "waiting_for_user" {
		writeJSONError(w, http.StatusConflict, "turn is no longer waiting for an answer")
		return
	}
	decided, err := queries.DecideUserQuestionRequest(r.Context(), sqlc.DecideUserQuestionRequestParams{
		ID:        requestID,
		Status:    status,
		Answers:   canonicalAnswers,
		Reply:     canonicalReply,
		DecidedBy: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "question request already decided")
			return
		}
		serverError(w, r, err)
		return
	}
	queued, err := queries.QueueWaitingTurnForQuestions(r.Context(), decided.TurnID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if queued != 1 {
		writeJSONError(w, http.StatusConflict, "turn is no longer waiting for an answer")
		return
	}
	entity, err := userQuestionRequestJSONRaw(decided.ID.String(), decided.TurnID.String(), decided.ToolCallID, decided.Status, decided.Questions, decided.Answers, decided.Reply, decided.CreatedAt.Time, nullableTime(decided.AnsweredAt))
	if err != nil {
		serverError(w, r, err)
		return
	}
	payload, err := json.Marshal(map[string]any{"question_request": entity})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'question_resolved', $2::jsonb)`, decided.TurnID, string(payload)); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"question_request": entity,
		"turn_id":          decided.TurnID.String(),
	})
}

func (a *App) replayUserQuestionDecision(w http.ResponseWriter, r *http.Request, tx pgx.Tx, requestID pgtype.UUID, questions []byte, body answerUserQuestionBody) {
	current, err := a.Queries.GetUserQuestionRequestByID(r.Context(), requestID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	status, canonicalAnswers, canonicalReply, err := aichattools.ValidateUserQuestionDecision(questions, body.Decision, body.Answers, body.Reply)
	if err != nil || status != current.Status || !userQuestionAnswersEqual(canonicalAnswers, current.Answers) || canonicalReply != current.Reply {
		writeJSONError(w, http.StatusConflict, "question request already decided")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	entity, err := userQuestionRequestJSONRaw(current.ID.String(), current.TurnID.String(), current.ToolCallID, current.Status, current.Questions, current.Answers, current.Reply, current.CreatedAt.Time, nullableTime(current.AnsweredAt))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"question_request": entity,
		"turn_id":          current.TurnID.String(),
	})
}

func userQuestionAnswersEqual(canonical, stored []byte) bool {
	blank := func(raw []byte) bool {
		return len(raw) == 0 || string(raw) == "null"
	}
	if blank(canonical) {
		return blank(stored)
	}
	var left, right any
	if err := json.Unmarshal(canonical, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(stored, &right); err != nil {
		return false
	}
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}
