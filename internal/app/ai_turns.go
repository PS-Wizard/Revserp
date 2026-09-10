package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

const (
	defaultAITurnModel       = "deepseek-flash"
	aiTurnMaxAttempts        = 2
	aiTurnMaxContentBytes    = 32768
	aiTurnMaxImages          = 4
	aiTurnMaxImageBytes      = 8 << 20
	aiTurnMaxTotalImageBytes = 12 << 20
	aiTurnSubmitMaxBodyBytes = 16 << 20
)

type aiTurnImage struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type aiTurnRequest struct {
	Content         string        `json:"content"`
	Images          []aiTurnImage `json:"images"`
	ReasoningEffort string        `json:"reasoning_effort"`
	CrawlID         *string       `json:"crawl_id"`
	ClientRequestID string        `json:"client_request_id"`
}

type acceptedAITurnImage struct {
	mediaType string
	data      string
	decoded   []byte
}

type acceptedAITurnRequest struct {
	content         string
	contentBlocks   []byte
	images          []acceptedAITurnImage
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
	if !readJSONOrRespondWithMaxBytes(w, r, &body, aiTurnSubmitMaxBodyBytes) {
		return
	}
	request, err := acceptAITurnRequest(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
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
	if len(body.Content) > aiTurnMaxContentBytes {
		return acceptedAITurnRequest{}, errInvalidTurnRequest
	}
	images, err := acceptAITurnImages(body.Images)
	if err != nil {
		return acceptedAITurnRequest{}, err
	}
	if strings.TrimSpace(body.Content) == "" && len(images) == 0 {
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
	contentBlocks, err := marshalAITurnContentBlocks(images)
	if err != nil {
		return acceptedAITurnRequest{}, errInvalidTurnRequest
	}
	return acceptedAITurnRequest{
		content:         body.Content,
		contentBlocks:   contentBlocks,
		images:          images,
		effort:          effort,
		suppliedCrawlID: suppliedCrawlID,
		clientRequestID: clientRequestID,
		requestHash:     aiTurnRequestHash(body.Content, effort, suppliedCrawlID, images),
	}, nil
}

func acceptAITurnImages(images []aiTurnImage) ([]acceptedAITurnImage, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > aiTurnMaxImages {
		return nil, errInvalidTurnRequest
	}
	accepted := make([]acceptedAITurnImage, 0, len(images))
	total := 0
	for _, image := range images {
		mediaType := strings.TrimSpace(image.MediaType)
		if !validAITurnMediaType(mediaType) {
			return nil, errInvalidTurnRequest
		}
		data := strings.TrimSpace(image.Data)
		if data == "" || looksLikeClientImageURL(data) {
			return nil, errInvalidTurnRequest
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(decoded) == 0 || len(decoded) > aiTurnMaxImageBytes {
			return nil, errInvalidTurnRequest
		}
		if sniffedImageMediaType(decoded) != mediaType {
			return nil, errInvalidTurnRequest
		}
		total += len(decoded)
		if total > aiTurnMaxTotalImageBytes {
			return nil, errInvalidTurnRequest
		}
		accepted = append(accepted, acceptedAITurnImage{
			mediaType: mediaType,
			data:      base64.StdEncoding.EncodeToString(decoded),
			decoded:   decoded,
		})
	}
	return accepted, nil
}

func validAITurnMediaType(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func looksLikeClientImageURL(data string) bool {
	return strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") || strings.HasPrefix(data, "data:")
}

func sniffedImageMediaType(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

func marshalAITurnContentBlocks(images []acceptedAITurnImage) ([]byte, error) {
	if len(images) == 0 {
		return nil, nil
	}
	blocks := make([]struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}, len(images))
	for i, image := range images {
		blocks[i].Type = "image"
		blocks[i].MediaType = image.mediaType
		blocks[i].Data = image.data
	}
	return json.Marshal(blocks)
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

func aiTurnRequestHash(content, effort string, crawlID pgtype.UUID, images []acceptedAITurnImage) []byte {
	hash := sha256.New()
	writeHashPrefixed(hash, []byte(content))
	writeHashPrefixed(hash, []byte(effort))
	writeHashPrefixed(hash, []byte(crawlID.String()))
	for _, image := range images {
		writeHashPrefixed(hash, image.decoded)
		writeHashPrefixed(hash, []byte(image.mediaType))
	}
	return hash.Sum(nil)
}

func writeHashPrefixed(w interface{ Write([]byte) (int, error) }, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write(value)
}

func (a *App) submitAITurn(ctx context.Context, userID, conversationID pgtype.UUID, request acceptedAITurnRequest) (aiTurnSubmission, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return aiTurnSubmission{}, fmt.Errorf("begin ai turn submission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	submission, err := a.submitAITurnTx(ctx, tx, userID, conversationID, request)
	if err != nil {
		if errors.Is(err, errConversationBusy) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return aiTurnSubmission{}, fmt.Errorf("commit recovered ai turn: %w", commitErr)
			}
			return aiTurnSubmission{}, errConversationBusy
		}
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

	if err := recoverExpiredAITurnsForConversation(ctx, queries, conversationID); err != nil {
		return aiTurnSubmission{}, err
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
	userMessageID, err := queries.CreateAIMessage(ctx, sqlc.CreateAIMessageParams{TurnID: turnID, Role: "user", Status: "complete", Content: request.content, ContentBlocks: request.contentBlocks})
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

// recoverExpiredAITurnsForConversation atomically recovers any expired running
// turn for the conversation using the same semantics as the periodic worker
// recovery (aichatworker.Worker.recover). Expired without output and with
// attempts remaining is requeued; otherwise it is failed and produces the same
// terminal durable event as worker recovery. The caller must hold
// LockAIConversationForTurn so concurrent submissions are serialized.
func recoverExpiredAITurnsForConversation(ctx context.Context, queries *sqlc.Queries, conversationID pgtype.UUID) error {
	recovered, err := queries.RecoverExpiredAITurnsForConversation(ctx, sqlc.RecoverExpiredAITurnsForConversationParams{
		MaxAttempts:    aiTurnMaxAttempts,
		ConversationID: conversationID,
	})
	if err != nil {
		return fmt.Errorf("recover expired ai turns: %w", err)
	}
	for _, turn := range recovered {
		if turn.Status != "failed" {
			continue
		}
		if !turn.IsPartial {
			if _, err := queries.FailPendingAssistantMessageForTurn(ctx, turn.ID); err != nil {
				return fmt.Errorf("mark failed assistant message: %w", err)
			}
		}
		if err := queries.CreateFailedAITurnEvent(ctx, turn.ID); err != nil {
			return fmt.Errorf("create failed ai turn event: %w", err)
		}
	}
	return nil
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
