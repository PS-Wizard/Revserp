package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiTurnResponse struct {
	ID               string               `json:"id"`
	ConversationID   string               `json:"conversation_id"`
	Status           string               `json:"status"`
	RequestedEffort  string               `json:"requested_effort"`
	EffectiveEffort  string               `json:"effective_effort"`
	Model            string               `json:"model"`
	AttemptCount     int32                `json:"attempt_count"`
	CancelRequested  bool                 `json:"cancel_requested"`
	PromptTokens     *int32               `json:"prompt_tokens"`
	ReasoningTokens  *int32               `json:"reasoning_tokens"`
	CompletionTokens *int32               `json:"completion_tokens"`
	TotalTokens      *int32               `json:"total_tokens"`
	ErrorCode        *string              `json:"error_code"`
	QueuedAt         time.Time            `json:"queued_at"`
	StartedAt        *time.Time           `json:"started_at"`
	CompletedAt      *time.Time           `json:"completed_at"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
	Messages         []aiMessageResponse  `json:"messages"`
	ToolCalls        []aiToolCallResponse `json:"tool_calls"`
}

type aiMessageResponse struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type aiTurnSnapshot struct {
	ID               pgtype.UUID
	ConversationID   pgtype.UUID
	Status           string
	RequestedEffort  string
	EffectiveEffort  string
	Model            string
	AttemptCount     int32
	CancelRequested  bool
	PromptTokens     pgtype.Int4
	ReasoningTokens  pgtype.Int4
	CompletionTokens pgtype.Int4
	TotalTokens      pgtype.Int4
	ErrorCode        pgtype.Text
	QueuedAt         pgtype.Timestamptz
	StartedAt        pgtype.Timestamptz
	CompletedAt      pgtype.Timestamptz
	CreatedAt        pgtype.Timestamptz
	UpdatedAt        pgtype.Timestamptz
}

func snapshotFromAITurn(row sqlc.GetAITurnForUserRow) aiTurnSnapshot {
	return aiTurnSnapshot{
		ID: row.ID, ConversationID: row.ConversationID, Status: row.Status,
		RequestedEffort: row.RequestedEffort, EffectiveEffort: row.EffectiveEffort, Model: row.Model,
		AttemptCount: row.AttemptCount, CancelRequested: row.CancelRequested,
		PromptTokens: row.PromptTokens, ReasoningTokens: row.ReasoningTokens,
		CompletionTokens: row.CompletionTokens, TotalTokens: row.TotalTokens, ErrorCode: row.ErrorCode,
		QueuedAt: row.QueuedAt, StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func snapshotFromLockedAITurn(row sqlc.LockAITurnForUserRow) aiTurnSnapshot {
	return aiTurnSnapshot{
		ID: row.ID, ConversationID: row.ConversationID, Status: row.Status,
		RequestedEffort: row.RequestedEffort, EffectiveEffort: row.EffectiveEffort, Model: row.Model,
		AttemptCount: row.AttemptCount, CancelRequested: row.CancelRequested,
		PromptTokens: row.PromptTokens, ReasoningTokens: row.ReasoningTokens,
		CompletionTokens: row.CompletionTokens, TotalTokens: row.TotalTokens, ErrorCode: row.ErrorCode,
		QueuedAt: row.QueuedAt, StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func newAITurnResponse(turn aiTurnSnapshot, messages []sqlc.ListAIMessagesForUserRow) aiTurnResponse {
	response := aiTurnResponse{
		ID:               turn.ID.String(),
		ConversationID:   turn.ConversationID.String(),
		Status:           turn.Status,
		RequestedEffort:  turn.RequestedEffort,
		EffectiveEffort:  turn.EffectiveEffort,
		Model:            turn.Model,
		AttemptCount:     turn.AttemptCount,
		CancelRequested:  turn.CancelRequested,
		PromptTokens:     nullableInt32(turn.PromptTokens),
		ReasoningTokens:  nullableInt32(turn.ReasoningTokens),
		CompletionTokens: nullableInt32(turn.CompletionTokens),
		TotalTokens:      nullableInt32(turn.TotalTokens),
		ErrorCode:        nullableString(turn.ErrorCode),
		QueuedAt:         turn.QueuedAt.Time,
		StartedAt:        nullableTime(turn.StartedAt),
		CompletedAt:      nullableTime(turn.CompletedAt),
		CreatedAt:        turn.CreatedAt.Time,
		UpdatedAt:        turn.UpdatedAt.Time,
		Messages:         make([]aiMessageResponse, 0, len(messages)),
		ToolCalls:        make([]aiToolCallResponse, 0),
	}
	for _, message := range messages {
		response.Messages = append(response.Messages, aiMessageResponse{
			ID: message.ID.String(), Role: message.Role, Status: message.Status, Content: message.Content,
			CreatedAt: message.CreatedAt.Time, UpdatedAt: message.UpdatedAt.Time,
		})
	}
	return response
}

func nullableInt32(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	return &value.Int32
}

func nullableString(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func (a *App) handleGetAITurn(w http.ResponseWriter, r *http.Request) {
	turnID, err := parseUUIDParam(chi.URLParam(r, "turnID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid turn id")
		return
	}
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}
	turn, err := a.Queries.GetAITurnForUser(r.Context(), sqlc.GetAITurnForUserParams{UserID: user.ID, TurnID: turnID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "turn not found")
		return
	}
	if err != nil {
		serverError(w, r, fmt.Errorf("get ai turn: %w", err))
		return
	}
	messages, err := a.Queries.ListAIMessagesForUser(r.Context(), sqlc.ListAIMessagesForUserParams{UserID: user.ID, TurnID: turnID})
	if err != nil {
		serverError(w, r, fmt.Errorf("get ai turn messages: %w", err))
		return
	}
	toolCalls, err := a.Queries.ListAIToolCallsForTurn(r.Context(), turnID)
	if err != nil {
		serverError(w, r, fmt.Errorf("get ai turn tool calls: %w", err))
		return
	}
	response := newAITurnResponse(snapshotFromAITurn(turn), messages)
	response.ToolCalls = newAIToolCallsResponse(toolCalls)
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleCancelAITurn(w http.ResponseWriter, r *http.Request) {
	turnID, err := parseUUIDParam(chi.URLParam(r, "turnID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid turn id")
		return
	}

	var response aiTurnResponse
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		serverError(w, r, err)
		return
	}
	locked, err := queries.LockAITurnForUser(r.Context(), sqlc.LockAITurnForUserParams{UserID: user.ID, TurnID: turnID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "turn not found")
		return
	}
	if err != nil {
		serverError(w, r, fmt.Errorf("authorize ai turn cancellation: %w", err))
		return
	}
	turn := snapshotFromLockedAITurn(locked)
	switch turn.Status {
	case "queued":
		changed, err := queries.StopQueuedAITurn(r.Context(), turnID)
		if err != nil {
			serverError(w, r, fmt.Errorf("stop queued ai turn: %w", err))
			return
		}
		if changed == 1 {
			if err := queries.MarkCancelledAssistantMessage(r.Context(), turnID); err != nil {
				serverError(w, r, fmt.Errorf("mark cancelled ai message: %w", err))
				return
			}
			if err := queries.CreateCancelledAITurnEvent(r.Context(), turnID); err != nil {
				serverError(w, r, fmt.Errorf("create cancelled ai event: %w", err))
				return
			}
		}
	case "running":
		if _, err := queries.RequestCancelRunningAITurn(r.Context(), turnID); err != nil {
			serverError(w, r, fmt.Errorf("request ai turn cancellation: %w", err))
			return
		}
	}
	updated, err := queries.GetAITurnForUser(r.Context(), sqlc.GetAITurnForUserParams{UserID: user.ID, TurnID: turnID})
	if err != nil {
		serverError(w, r, fmt.Errorf("read cancelled ai turn: %w", err))
		return
	}
	messages, err := queries.ListAIMessagesForUser(r.Context(), sqlc.ListAIMessagesForUserParams{UserID: user.ID, TurnID: turnID})
	if err != nil {
		serverError(w, r, fmt.Errorf("read cancelled ai messages: %w", err))
		return
	}
	response = newAITurnResponse(snapshotFromAITurn(updated), messages)
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func parseAITurnEventCursor(r *http.Request) (int64, error) {
	value, ok := r.URL.Query()["after"]
	if !ok {
		value = []string{r.Header.Get("Last-Event-ID")}
	}
	if len(value) == 0 || value[0] == "" {
		if ok {
			return 0, errors.New("empty cursor")
		}
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value[0], 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("invalid cursor")
	}
	return cursor, nil
}

func (a *App) handleGetAITurnEvents(w http.ResponseWriter, r *http.Request) {
	cursor, err := parseAITurnEventCursor(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid event cursor")
		return
	}
	turnID, err := parseUUIDParam(chi.URLParam(r, "turnID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid turn id")
		return
	}
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}
	_, err = a.Queries.GetAITurnForUser(r.Context(), sqlc.GetAITurnForUserParams{UserID: user.ID, TurnID: turnID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "turn not found")
		return
	}
	if err != nil {
		serverError(w, r, fmt.Errorf("authorize ai turn events: %w", err))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		serverError(w, r, fmt.Errorf("clear sse write deadline: %w", err))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		// Observe status before every event query so a terminal commit cannot hide its event.
		latest, err := a.Queries.GetAITurnForUser(r.Context(), sqlc.GetAITurnForUserParams{UserID: user.ID, TurnID: turnID})
		if err != nil {
			return
		}
		terminal := latest.Status == "completed" || latest.Status == "stopped" || latest.Status == "failed"
		events, err := a.listAITurnEvents(r.Context(), user.ID, turnID, cursor)
		if err != nil {
			return
		}
		if len(events) > 0 && !writeAITurnSSEBatch(w, flusher, events, &cursor) {
			return
		}
		if terminal {
			// Drain once more after the observed terminal status and event batch.
			tail, err := a.listAITurnEvents(r.Context(), user.ID, turnID, cursor)
			if err != nil || len(tail) == 0 {
				return
			}
			if !writeAITurnSSEBatch(w, flusher, tail, &cursor) {
				return
			}
			continue
		}
		if len(events) > 0 {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *App) listAITurnEvents(ctx context.Context, userID, turnID pgtype.UUID, cursor int64) ([]sqlc.ListAITurnEventsForUserRow, error) {
	return a.Queries.ListAITurnEventsForUser(ctx, sqlc.ListAITurnEventsForUserParams{UserID: userID, TurnID: turnID, AfterID: cursor})
}

func writeAITurnSSEBatch(w http.ResponseWriter, flusher http.Flusher, events []sqlc.ListAITurnEventsForUserRow, cursor *int64) bool {
	for _, event := range events {
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.EventType, event.Payload); err != nil {
			return false
		}
		*cursor = event.ID
	}
	flusher.Flush()
	return true
}
