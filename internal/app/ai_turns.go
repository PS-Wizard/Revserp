package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const defaultAITurnModel = "deepseek-v4-flash"

type aiTurnRequest struct {
	Content         string  `json:"content"`
	ReasoningEffort string  `json:"reasoning_effort"`
	CrawlID         *string `json:"crawl_id"`
	ClientRequestID string  `json:"client_request_id"`
}

type acceptedAITurnRequest struct {
	content         string
	effort          string
	suppliedCrawlID pgtype.UUID
	clientRequestID string
	requestHash     []byte
}

type aiTurnSubmission struct {
	ConversationID     pgtype.UUID
	TurnID             pgtype.UUID
	UserMessageID      pgtype.UUID
	AssistantMessageID pgtype.UUID
}

type aiTurnSubmissionResponse struct {
	ConversationID     string `json:"conversation_id"`
	TurnID             string `json:"turn_id"`
	UserMessageID      string `json:"user_message_id"`
	AssistantMessageID string `json:"assistant_message_id"`
	Status             string `json:"status"`
}

type turnSubmissionError string

func (err turnSubmissionError) Error() string { return string(err) }

const (
	errInvalidTurnRequest   = turnSubmissionError("invalid_request")
	errInvalidCrawl         = turnSubmissionError("invalid_crawl")
	errConversationNotFound = turnSubmissionError("conversation_not_found")
	errAIChatDisabled       = turnSubmissionError("ai_chat_disabled")
	errReasoningNotAllowed  = turnSubmissionError("reasoning_not_allowed")
	errIdempotencyConflict  = turnSubmissionError("idempotency_conflict")
	errConversationBusy     = turnSubmissionError("conversation_busy")
	errMonthlyMessageLimit  = turnSubmissionError("monthly_message_limit_reached")
)

// handleSubmitAITurn accepts one durable user turn without waiting for a provider.
func (a *App) handleSubmitAITurn(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	var body aiTurnRequest
	if err := readJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	request, err := acceptAITurnRequest(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}
	submission, err := a.submitAITurn(r.Context(), user.ID, conversationID, request)
	if err != nil {
		if writeAITurnSubmissionError(w, err) {
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, newAITurnSubmissionResponse(submission))
}

func acceptAITurnRequest(body aiTurnRequest) (acceptedAITurnRequest, error) {
	if strings.TrimSpace(body.Content) == "" || len(body.Content) > 32768 {
		return acceptedAITurnRequest{}, errInvalidTurnRequest
	}
	clientRequestID := strings.TrimSpace(body.ClientRequestID)
	if clientRequestID == "" || len(clientRequestID) > 128 {
		return acceptedAITurnRequest{}, errInvalidTurnRequest
	}
	effort, ok := normalizeAITurnEffort(body.ReasoningEffort)
	if !ok {
		return acceptedAITurnRequest{}, errInvalidTurnRequest
	}

	var suppliedCrawlID pgtype.UUID
	if body.CrawlID != nil {
		if err := suppliedCrawlID.Scan(strings.TrimSpace(*body.CrawlID)); err != nil {
			return acceptedAITurnRequest{}, errInvalidCrawl
		}
	}
	return acceptedAITurnRequest{
		content:         body.Content,
		effort:          effort,
		suppliedCrawlID: suppliedCrawlID,
		clientRequestID: clientRequestID,
		requestHash:     aiTurnRequestHash(body.Content, effort, suppliedCrawlID),
	}, nil
}

func normalizeAITurnEffort(value string) (string, bool) {
	effort := strings.ToLower(strings.TrimSpace(value))
	switch effort {
	case "medium", "xhigh":
		return "high", true
	case "none", "low", "high", "max":
		return effort, true
	default:
		return "", false
	}
}

func aiTurnRequestHash(content, effort string, crawlID pgtype.UUID) []byte {
	hash := sha256.New()
	for _, value := range []string{content, effort, crawlID.String()} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return hash.Sum(nil)
}

func (a *App) submitAITurn(ctx context.Context, userID, conversationID pgtype.UUID, request acceptedAITurnRequest) (aiTurnSubmission, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("begin ai turn submission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	submission, err := a.submitAITurnTx(ctx, tx, userID, conversationID, request)
	if err != nil {
		if isAITurnIdempotencyUniqueError(err) {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return aiTurnSubmission{}, fmt.Errorf("rollback conflicting ai turn submission: %w", rollbackErr)
			}
			return a.findExistingAITurnSubmission(ctx, userID, conversationID, request)
		}
		if isAITurnActiveUniqueError(err) {
			return aiTurnSubmission{}, errConversationBusy
		}
		return aiTurnSubmission{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return aiTurnSubmission{}, fmt.Errorf("commit ai turn submission: %w", err)
	}
	return submission, nil
}

func (a *App) submitAITurnTx(ctx context.Context, tx pgx.Tx, userID, conversationID pgtype.UUID, request acceptedAITurnRequest) (aiTurnSubmission, error) {
	queries := a.Queries.WithTx(tx)
	conversation, err := queries.LockAIConversationForTurn(ctx, sqlc.LockAIConversationForTurnParams{
		ConversationID: conversationID,
		UserID:         userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return aiTurnSubmission{}, errConversationNotFound
		}
		return aiTurnSubmission{}, fmt.Errorf("lock ai conversation: %w", err)
	}

	if existing, err := queries.FindAITurnByClientRequestID(ctx, sqlc.FindAITurnByClientRequestIDParams{
		ConversationID:  conversationID,
		UserID:          userID,
		ClientRequestID: request.clientRequestID,
	}); err == nil {
		return submissionFromExisting(conversationID, existing, request.requestHash)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return aiTurnSubmission{}, fmt.Errorf("find idempotent ai turn: %w", err)
	}

	if !conversation.AiChat {
		return aiTurnSubmission{}, errAIChatDisabled
	}
	if !containsString(conversation.AiAllowedReasoningEfforts, request.effort) {
		return aiTurnSubmission{}, errReasoningNotAllowed
	}

	var resolvedCrawlID pgtype.UUID
	if request.suppliedCrawlID.Valid {
		resolvedCrawlID, err = queries.GetCompletedCrawlForProject(ctx, sqlc.GetCompletedCrawlForProjectParams{
			CrawlID:   request.suppliedCrawlID,
			ProjectID: conversation.ProjectID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return aiTurnSubmission{}, errInvalidCrawl
		}
		if err != nil {
			return aiTurnSubmission{}, fmt.Errorf("validate ai turn crawl: %w", err)
		}
	} else {
		resolvedCrawlID, err = queries.GetLatestCompletedCrawlForProject(ctx, conversation.ProjectID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return aiTurnSubmission{}, fmt.Errorf("find latest completed crawl: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			resolvedCrawlID = pgtype.UUID{}
		}
	}

	busy, err := queries.HasActiveAITurnForConversation(ctx, conversationID)
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("check active ai turn: %w", err)
	}
	if busy {
		return aiTurnSubmission{}, errConversationBusy
	}
	if _, err := queries.ReserveAIWorkspaceMonthlyMessage(ctx, sqlc.ReserveAIWorkspaceMonthlyMessageParams{
		OrganizationID: conversation.OrganizationID,
		MonthlyLimit:   conversation.AiMonthlyMessageLimit,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return aiTurnSubmission{}, errMonthlyMessageLimit
		}
		return aiTurnSubmission{}, fmt.Errorf("reserve ai workspace quota: %w", err)
	}

	turnID, err := queries.CreateAITurn(ctx, sqlc.CreateAITurnParams{
		ConversationID:  conversationID,
		UserID:          userID,
		RequestedEffort: request.effort,
		EffectiveEffort: request.effort,
		Model:           aiTurnModel(a.Config.DeepSeekModel),
		CrawlID:         resolvedCrawlID,
		ClientRequestID: request.clientRequestID,
		RequestHash:     request.requestHash,
		DisabledAiTools: normalizeDisabledAITools(conversation.DisabledAiTools, conversation.GscConnector),
	})
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("create ai turn: %w", err)
	}
	userMessageID, err := queries.CreateAIMessage(ctx, sqlc.CreateAIMessageParams{TurnID: turnID, Role: "user", Status: "complete", Content: request.content})
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("create user ai message: %w", err)
	}
	assistantMessageID, err := queries.CreateAIMessage(ctx, sqlc.CreateAIMessageParams{TurnID: turnID, Role: "assistant", Status: "pending", Content: ""})
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("create assistant ai message: %w", err)
	}
	if err := queries.TouchAIConversation(ctx, conversationID); err != nil {
		return aiTurnSubmission{}, fmt.Errorf("touch ai conversation: %w", err)
	}
	return aiTurnSubmission{ConversationID: conversationID, TurnID: turnID, UserMessageID: userMessageID, AssistantMessageID: assistantMessageID}, nil
}

func (a *App) findExistingAITurnSubmission(ctx context.Context, userID, conversationID pgtype.UUID, request acceptedAITurnRequest) (aiTurnSubmission, error) {
	existing, err := a.Queries.FindAITurnByClientRequestID(ctx, sqlc.FindAITurnByClientRequestIDParams{
		ConversationID:  conversationID,
		UserID:          userID,
		ClientRequestID: request.clientRequestID,
	})
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("re-read idempotent ai turn: %w", err)
	}
	return submissionFromExisting(conversationID, existing, request.requestHash)
}

func submissionFromExisting(conversationID pgtype.UUID, existing sqlc.FindAITurnByClientRequestIDRow, requestHash []byte) (aiTurnSubmission, error) {
	if !bytes.Equal(existing.RequestHash, requestHash) {
		return aiTurnSubmission{}, errIdempotencyConflict
	}
	return aiTurnSubmission{ConversationID: conversationID, TurnID: existing.TurnID, UserMessageID: existing.UserMessageID, AssistantMessageID: existing.AssistantMessageID}, nil
}

func aiTurnModel(model string) string {
	if model = strings.TrimSpace(model); model == "" {
		return defaultAITurnModel
	}
	return model
}

// aiToolCallResponse is one executed tool call in the turn detail response.
// Args stays a raw JSON object so clients render the model's exact arguments.
type aiToolCallResponse struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args"`
	Status    string          `json:"status"`
	Summary   string          `json:"summary"`
	Seq       int32           `json:"seq"`
	CreatedAt time.Time       `json:"created_at"`
}

// newAIToolCallsResponse maps the durable tool log to the turn detail shape.
func newAIToolCallsResponse(rows []sqlc.ListAIToolCallsForTurnRow) []aiToolCallResponse {
	calls := make([]aiToolCallResponse, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, aiToolCallResponse{
			CallID:    row.CallID,
			Name:      row.Name,
			Args:      json.RawMessage(row.Args),
			Status:    row.Status,
			Summary:   row.Summary,
			Seq:       row.Seq,
			CreatedAt: row.CreatedAt.Time,
		})
	}
	return calls
}

func isAITurnIdempotencyUniqueError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		strings.HasPrefix(pgErr.ConstraintName, "ai_turns_conversation_id_created_by_user_id_client_request_id")
}

func isAITurnActiveUniqueError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_ai_turns_one_active_per_conversation"
}

func writeAITurnSubmissionError(w http.ResponseWriter, err error) bool {
	code, ok := err.(turnSubmissionError)
	if !ok {
		return false
	}
	switch code {
	case errInvalidTurnRequest, errInvalidCrawl:
		writeJSONError(w, http.StatusBadRequest, code.Error())
	case errConversationNotFound:
		writeJSONError(w, http.StatusNotFound, "conversation not found")
	case errAIChatDisabled, errReasoningNotAllowed:
		writeJSONError(w, http.StatusForbidden, code.Error())
	case errIdempotencyConflict, errConversationBusy:
		writeJSONError(w, http.StatusConflict, code.Error())
	case errMonthlyMessageLimit:
		writeJSONError(w, http.StatusTooManyRequests, code.Error())
	default:
		return false
	}
	return true
}

func newAITurnSubmissionResponse(submission aiTurnSubmission) aiTurnSubmissionResponse {
	return aiTurnSubmissionResponse{
		ConversationID:     submission.ConversationID.String(),
		TurnID:             submission.TurnID.String(),
		UserMessageID:      submission.UserMessageID.String(),
		AssistantMessageID: submission.AssistantMessageID.String(),
		Status:             "queued",
	}
}
