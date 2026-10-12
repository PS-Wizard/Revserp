package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// errUserQuestionStopRaced finalizes stopped with no provider request.
var errUserQuestionStopRaced = errors.New("stop raced user question resume")

// userQuestionResumePrompt lives only in the resumed provider request.
const userQuestionResumePrompt = "The user answered your questions above. Reconsider your next step using their answers: redo any plan they superseded rather than executing actions you prepared before asking."

const deferredUserQuestionSummary = "deferred while waiting for the user's answer; not executed"

// userQuestionMessage mirrors ai.Message including ReasoningContent, which
// ai.Message drops from its own JSON encoding.
type userQuestionMessage struct {
	Role             string        `json:"role"`
	Content          string        `json:"content,omitempty"`
	Images           []ai.Image    `json:"images,omitempty"`
	ToolCalls        []ai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	Name             string        `json:"name,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
}

func snapshotUserQuestionMessages(live []ai.Message) []userQuestionMessage {
	messages := make([]userQuestionMessage, 0, len(live))
	for _, message := range live {
		messages = append(messages, userQuestionMessage{
			Role:             string(message.Role),
			Content:          message.Content,
			Images:           message.Images,
			ToolCalls:        message.ToolCalls,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
			ReasoningContent: message.ReasoningContent,
		})
	}
	return messages
}

func restoreUserQuestionMessages(stored []userQuestionMessage) []ai.Message {
	live := make([]ai.Message, 0, len(stored))
	for _, message := range stored {
		live = append(live, ai.Message{
			Role:             ai.Role(message.Role),
			Content:          message.Content,
			Images:           message.Images,
			ToolCalls:        message.ToolCalls,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
			ReasoningContent: message.ReasoningContent,
		})
	}
	return live
}

// userQuestionBudgets snapshots remainders, never fresh allowances.
type userQuestionBudgets struct {
	Row         int                            `json:"row"`
	PageContent aichattools.PageContentState   `json:"page_content"`
	Web         aichattools.WebBudgetState     `json:"web"`
	Suggest     aichattools.SuggestBudgetState `json:"suggest"`
	Skill       int                            `json:"skill"`
}

func snapshotUserQuestionBudgets(scope aichattools.Scope) userQuestionBudgets {
	return userQuestionBudgets{
		Row:         scope.RowBudget.State(),
		PageContent: scope.PageContentBudget.State(),
		Web:         scope.WebBudget.State(),
		Suggest:     scope.SuggestBudget.State(),
		Skill:       scope.SkillsBudget.State(),
	}
}

func (b userQuestionBudgets) restore() (row *aichattools.Budget, page *aichattools.PageContentBudget, web *aichattools.WebBudget, suggest *aichattools.SuggestBudget, skill *aichattools.SkillBudget) {
	return aichattools.RestoreBudget(b.Row),
		aichattools.RestorePageContentBudget(b.PageContent),
		aichattools.RestoreWebBudget(b.Web),
		aichattools.RestoreSuggestBudget(b.Suggest),
		aichattools.RestoreSkillBudget(b.Skill)
}

func deferredUserQuestionResult(call ai.ToolCall) (string, aichattools.Result) {
	content := fmt.Sprintf("tool %q call %q was %s. %s.", call.Name, call.ID, deferredUserQuestionSummary, userQuestionResumePrompt)
	return "failed", aichattools.Result{Content: content, Summary: deferredUserQuestionSummary}
}

func (w *Worker) parkTurnForUserQuestion(ctx context.Context, claimed turn, call ai.ToolCall, questions []aichattools.AskUserQuestion, live []ai.Message, usage ai.Usage, budgets userQuestionBudgets, rundown map[string]string, deferred []ai.ToolCall, round int, nextSeq int32) (bool, error) {
	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		return false, fmt.Errorf("encode user questions: %w", err)
	}
	messagesJSON, err := json.Marshal(snapshotUserQuestionMessages(live))
	if err != nil {
		return false, fmt.Errorf("encode user question transcript: %w", err)
	}
	usageJSON, err := json.Marshal(usage)
	if err != nil {
		return false, fmt.Errorf("encode user question usage: %w", err)
	}
	budgetsJSON, err := json.Marshal(budgets)
	if err != nil {
		return false, fmt.Errorf("encode user question budgets: %w", err)
	}
	rundownJSON, err := json.Marshal(rundown)
	if err != nil {
		return false, fmt.Errorf("encode user question rundown: %w", err)
	}
	deferredJSON, err := json.Marshal(deferred)
	if err != nil {
		return false, fmt.Errorf("encode deferred user question calls: %w", err)
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := sqlc.New(tx)

	if _, err := queries.InsertAIToolCall(ctx, sqlc.InsertAIToolCallParams{
		TurnID: claimed.ID,
		Seq:    nextSeq,
		CallID: call.ID,
		Name:   call.Name,
		Args:   []byte(call.Args),
		Status: "awaiting",
	}); err != nil {
		return false, fmt.Errorf("insert user question tool call: %w", err)
	}

	created, err := queries.CreateUserQuestionRequest(ctx, sqlc.CreateUserQuestionRequestParams{
		TurnID:     claimed.ID,
		ToolCallID: call.ID,
		Questions:  questionsJSON,
	})
	if err != nil {
		return false, fmt.Errorf("insert user question request: %w", err)
	}
	checkpointed, err := queries.InsertUserQuestionCheckpoint(ctx, sqlc.InsertUserQuestionCheckpointParams{
		TurnID:            claimed.ID,
		QuestionRequestID: created.ID,
		Messages:          messagesJSON,
		Usage:             usageJSON,
		Budgets:           budgetsJSON,
		Rundown:           rundownJSON,
		DeferredCalls:     deferredJSON,
		Round:             int32(round),
		NextSeq:           nextSeq + 1,
	})
	if err != nil {
		return false, fmt.Errorf("insert user question checkpoint: %w", err)
	}
	if checkpointed != 1 {
		return false, fmt.Errorf("user question checkpoint already exists for turn %s", claimed.ID.String())
	}
	questionEntity, err := marshalUserQuestionEvent(created.ID.String(), created.TurnID.String(), created.ToolCallID, created.Status, created.Questions, created.Answers, created.Reply, created.CreatedAt.Time, nil)
	if err != nil {
		return false, fmt.Errorf("encode user question event: %w", err)
	}
	toolCallEvent, err := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "args": json.RawMessage(call.Args)})
	if err != nil {
		return false, fmt.Errorf("encode user question tool call event: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'tool_call', $2::jsonb), ($1, 'question_required', $3::jsonb)`, claimed.ID, string(toolCallEvent), string(questionEntity)); err != nil {
		return false, fmt.Errorf("insert user question events: %w", err)
	}
	parked, err := queries.ParkRunningTurnForQuestions(ctx, sqlc.ParkRunningTurnForQuestionsParams{
		TurnID:    claimed.ID,
		ClaimedBy: pgtype.Text{String: w.cfg.ID, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("park turn for user question: %w", err)
	}
	if parked != 1 {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	log.Printf("ai chat turn parked for user question: worker_id=%s turn_id=%s call_id=%s questions=%d deferred=%d", w.cfg.ID, claimed.ID.String(), call.ID, len(questions), len(deferred))
	return true, nil
}

type resumedUserQuestion struct {
	live  []ai.Message
	usage ai.Usage
	// added is the turn-local tail resume appended to live: the answered
	// question result and deferred sibling results. The synthetic resume
	// prompt steers live only and stays out of the durable transcript.
	added   []ai.Message
	budgets userQuestionBudgets
	rundown map[string]string
	round   int
}

func (w *Worker) resumeUserQuestionIfParked(ctx context.Context, claimed turn, registry *aichattools.Registry) (*resumedUserQuestion, error) {
	var checkpoint sqlc.GetUserQuestionCheckpointRow
	if err := w.pool.QueryRow(ctx, `SELECT turn_id, question_request_id, messages, usage, budgets, rundown, deferred_calls, round, next_seq FROM ai_user_question_checkpoints WHERE turn_id = $1`, claimed.ID).Scan(
		&checkpoint.TurnID, &checkpoint.QuestionRequestID, &checkpoint.Messages, &checkpoint.Usage, &checkpoint.Budgets, &checkpoint.Rundown, &checkpoint.DeferredCalls, &checkpoint.Round, &checkpoint.NextSeq,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		if isUndefinedTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read user question checkpoint: %w", err)
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var turnStatus string
	var turnOwner pgtype.Text
	var turnCancel pgtype.Timestamptz
	var leaseLive bool
	if err := tx.QueryRow(ctx, `SELECT status, claimed_by, cancel_requested_at, (lease_expires_at > now()) AS lease_live FROM ai_turns WHERE id = $1 FOR UPDATE`, claimed.ID).Scan(&turnStatus, &turnOwner, &turnCancel, &leaseLive); err != nil {
		return nil, fmt.Errorf("lock resuming turn: %w", err)
	}
	if turnCancel.Valid {
		return nil, errUserQuestionStopRaced
	}
	if turnStatus != "running" || !turnOwner.Valid || turnOwner.String != w.cfg.ID || !leaseLive {
		return nil, fmt.Errorf("resuming turn %s is not this worker's live claim", claimed.ID.String())
	}
	if _, ok := registry.Get(aichattools.AskUserQuestionName); !ok {
		return nil, w.cancelDisabledQuestionTx(ctx, tx, claimed.ID, "ask_user_question disabled while waiting")
	}
	var disabled []string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(f.disabled_ai_tools, ARRAY[]::TEXT[]) FROM ai_turns AS t JOIN ai_conversations AS c ON c.id = t.conversation_id JOIN projects AS p ON p.id = c.project_id LEFT JOIN organization_features AS f ON f.org_id = p.organization_id WHERE t.id = $1`, claimed.ID).Scan(&disabled); err != nil {
		return nil, fmt.Errorf("recheck user question access: %w", err)
	}
	for _, name := range disabled {
		if name == aichattools.AskUserQuestionName {
			return nil, w.cancelDisabledQuestionTx(ctx, tx, claimed.ID, "ask_user_question disabled while waiting")
		}
	}
	if checkpoint.TurnID != claimed.ID {
		return nil, fmt.Errorf("user question checkpoint belongs to another turn")
	}
	var request sqlc.GetUserQuestionRequestByIDRow
	if err := tx.QueryRow(ctx, `SELECT id, turn_id, tool_call_id, questions, answers, reply, status, decided_by, created_at, answered_at FROM ai_user_question_requests WHERE id = $1 FOR UPDATE`, checkpoint.QuestionRequestID).Scan(
		&request.ID, &request.TurnID, &request.ToolCallID, &request.Questions, &request.Answers, &request.Reply, &request.Status, &request.DecidedBy, &request.CreatedAt, &request.AnsweredAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			if _, err := tx.Exec(ctx, `DELETE FROM ai_user_question_checkpoints WHERE turn_id = $1`, claimed.ID); err != nil {
				return nil, fmt.Errorf("drop orphaned user question checkpoint: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("user question request gone for turn %s", claimed.ID.String())
		}
		return nil, fmt.Errorf("lock user question request: %w", err)
	}
	if request.TurnID != claimed.ID {
		return nil, fmt.Errorf("user question request belongs to another turn")
	}
	if request.Status != "answered" && request.Status != "skipped" {
		if err := CancelPendingUserQuestionsForTurn(ctx, sqlc.New(tx), claimed.ID, pgtype.UUID{}); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("turn %s requeued without a resolved question", claimed.ID.String())
	}

	var storedMessages []userQuestionMessage
	if err := json.Unmarshal(checkpoint.Messages, &storedMessages); err != nil {
		return nil, fmt.Errorf("decode user question transcript: %w", err)
	}
	var usage ai.Usage
	if len(checkpoint.Usage) > 0 {
		if err := json.Unmarshal(checkpoint.Usage, &usage); err != nil {
			return nil, fmt.Errorf("decode user question usage: %w", err)
		}
	}
	var budgets userQuestionBudgets
	if err := json.Unmarshal(checkpoint.Budgets, &budgets); err != nil {
		return nil, fmt.Errorf("decode user question budgets: %w", err)
	}
	rundown := map[string]string{}
	if len(checkpoint.Rundown) > 0 {
		if err := json.Unmarshal(checkpoint.Rundown, &rundown); err != nil {
			return nil, fmt.Errorf("decode user question rundown: %w", err)
		}
	}
	var deferred []ai.ToolCall
	if len(checkpoint.DeferredCalls) > 0 {
		if err := json.Unmarshal(checkpoint.DeferredCalls, &deferred); err != nil {
			return nil, fmt.Errorf("decode deferred user question calls: %w", err)
		}
	}
	var questions []aichattools.AskUserQuestion
	if err := json.Unmarshal(request.Questions, &questions); err != nil {
		return nil, fmt.Errorf("decode stored user questions: %w", err)
	}
	var answers []aichattools.UserQuestionAnswer
	if len(request.Answers) > 0 {
		if err := json.Unmarshal(request.Answers, &answers); err != nil {
			return nil, fmt.Errorf("decode stored user answers: %w", err)
		}
	}
	content, summary := aichattools.FormatUserQuestionToolResult(questions, request.Status, answers, request.Reply)
	content = capToolResultContent(content)

	var questionRowID pgtype.UUID
	var questionRowStatus string
	if err := tx.QueryRow(ctx, `SELECT id, status FROM ai_tool_calls WHERE turn_id = $1 AND call_id = $2`, claimed.ID, request.ToolCallID).Scan(&questionRowID, &questionRowStatus); err != nil {
		return nil, fmt.Errorf("read parked user question call: %w", err)
	}
	if questionRowStatus != "awaiting" {
		return nil, fmt.Errorf("parked user question call %s is %s, not awaiting", request.ToolCallID, questionRowStatus)
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_tool_calls SET status = 'completed', result_content = $2, summary = $3, updated_at = now() WHERE id = $1`, questionRowID, content, summary); err != nil {
		return nil, fmt.Errorf("complete parked user question call: %w", err)
	}
	questionResultEvent, err := json.Marshal(map[string]string{"id": request.ToolCallID, "name": aichattools.AskUserQuestionName, "summary": summary, "status": "completed"})
	if err != nil {
		return nil, fmt.Errorf("encode resumed user question result: %w", err)
	}
	if tag, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) SELECT id, 'tool_result', $2::jsonb FROM ai_turns WHERE id = $1 AND status = 'running' AND claimed_by = $3 AND lease_expires_at > now()`, claimed.ID, string(questionResultEvent), w.cfg.ID); err != nil {
		return nil, fmt.Errorf("insert resumed user question result: %w", err)
	} else if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("resuming turn %s lost its claim while writing events", claimed.ID.String())
	}

	live := restoreUserQuestionMessages(storedMessages)
	answered := ai.Message{Role: ai.RoleTool, Content: content, ToolCallID: request.ToolCallID, Name: aichattools.AskUserQuestionName}
	live = append(live, answered)
	added := []ai.Message{answered}
	recordToolRundown(rundown, request.ToolCallID, "completed", summary)
	for _, sibling := range deferred {
		status, result := deferredUserQuestionResult(sibling)
		result.Content = capToolResultContent(result.Content)
		siblingArgs := sibling.Args
		if !json.Valid([]byte(siblingArgs)) {
			siblingArgs = "{}"
		}
		var siblingRowID pgtype.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO ai_tool_calls (turn_id, seq, call_id, name, args, status) VALUES ($1, (SELECT COALESCE(MAX(seq), -1) + 1 FROM ai_tool_calls WHERE turn_id = $1), $2, $3, $4::jsonb, 'running') RETURNING id`, claimed.ID, sibling.ID, sibling.Name, siblingArgs).Scan(&siblingRowID); err != nil {
			return nil, fmt.Errorf("insert deferred user question call: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ai_tool_calls SET status = $2, result_content = $3, summary = $4, updated_at = now() WHERE id = $1`, siblingRowID, status, result.Content, result.Summary); err != nil {
			return nil, fmt.Errorf("complete deferred user question call: %w", err)
		}
		siblingCallEvent, err := json.Marshal(map[string]any{"id": sibling.ID, "name": sibling.Name, "args": json.RawMessage(siblingArgs)})
		if err != nil {
			return nil, fmt.Errorf("encode deferred user question call: %w", err)
		}
		siblingResultEvent, err := json.Marshal(map[string]string{"id": sibling.ID, "name": sibling.Name, "summary": result.Summary, "status": status})
		if err != nil {
			return nil, fmt.Errorf("encode deferred user question result: %w", err)
		}
		if tag, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) SELECT id, 'tool_call', $2::jsonb FROM ai_turns WHERE id = $1 AND status = 'running' AND claimed_by = $3 AND lease_expires_at > now()`, claimed.ID, string(siblingCallEvent), w.cfg.ID); err != nil {
			return nil, fmt.Errorf("insert deferred user question call event: %w", err)
		} else if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("resuming turn %s lost its claim while writing events", claimed.ID.String())
		}
		if tag, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) SELECT id, 'tool_result', $2::jsonb FROM ai_turns WHERE id = $1 AND status = 'running' AND claimed_by = $3 AND lease_expires_at > now()`, claimed.ID, string(siblingResultEvent), w.cfg.ID); err != nil {
			return nil, fmt.Errorf("insert deferred user question result event: %w", err)
		} else if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("resuming turn %s lost its claim while writing events", claimed.ID.String())
		}
		siblingResult := ai.Message{Role: ai.RoleTool, Content: result.Content, ToolCallID: sibling.ID, Name: sibling.Name}
		live = append(live, siblingResult)
		added = append(added, siblingResult)
		recordToolRundown(rundown, sibling.ID, status, result.Summary)
	}
	live = append(live, ai.Message{Role: ai.RoleUser, Content: userQuestionResumePrompt})
	if _, err := tx.Exec(ctx, `DELETE FROM ai_user_question_checkpoints WHERE turn_id = $1`, claimed.ID); err != nil {
		return nil, fmt.Errorf("drop resumed user question checkpoint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	log.Printf("ai chat turn resumed from user question: worker_id=%s turn_id=%s call_id=%s deferred=%d", w.cfg.ID, claimed.ID.String(), request.ToolCallID, len(deferred))
	return &resumedUserQuestion{live: live, usage: usage, added: added, budgets: budgets, rundown: rundown, round: int(checkpoint.Round) + 1}, nil
}

func (w *Worker) cancelDisabledQuestionTx(ctx context.Context, tx pgx.Tx, turnID pgtype.UUID, reason string) error {
	if err := CancelPendingUserQuestionsForTurn(ctx, sqlc.New(tx), turnID, pgtype.UUID{}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	log.Printf("ai chat parked question cancelled: worker_id=%s turn_id=%s reason=%s", w.cfg.ID, turnID.String(), reason)
	return fmt.Errorf("%s for turn %s", reason, turnID.String())
}

func marshalUserQuestionEvent(id, turnID, toolCallID, status string, questions, answers []byte, reply string, createdAt time.Time, answeredAt *time.Time) ([]byte, error) {
	entity, err := aichattools.MarshalUserQuestionRequest(id, turnID, toolCallID, status, questions, answers, reply, createdAt, answeredAt)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{"question_request": entity})
}

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func nullableCheckpointTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	moment := value.Time
	return &moment
}

// CancelPendingUserQuestionsForTurn cancels pending requests with resolved
// events and drops the checkpoint, executing nothing.
func CancelPendingUserQuestionsForTurn(ctx context.Context, queries *sqlc.Queries, turnID pgtype.UUID, decidedBy pgtype.UUID) error {
	exist, err := queries.UserQuestionTablesExist(ctx)
	if err != nil {
		return fmt.Errorf("probe user question tables: %w", err)
	}
	if !exist.Bool {
		return nil
	}
	cancelled, err := queries.CancelPendingUserQuestionsForTurn(ctx, sqlc.CancelPendingUserQuestionsForTurnParams{
		TurnID:    turnID,
		DecidedBy: decidedBy,
	})
	if err != nil {
		return fmt.Errorf("cancel pending user questions: %w", err)
	}
	for _, row := range cancelled {
		entity, err := marshalUserQuestionEvent(row.ID.String(), row.TurnID.String(), row.ToolCallID, row.Status, row.Questions, row.Answers, row.Reply, row.CreatedAt.Time, nullableCheckpointTime(row.AnsweredAt))
		if err != nil {
			return fmt.Errorf("encode cancelled user question: %w", err)
		}
		if err := queries.InsertAITurnEvent(ctx, sqlc.InsertAITurnEventParams{TurnID: turnID, EventType: "question_resolved", Payload: entity}); err != nil {
			return fmt.Errorf("insert cancelled user question event: %w", err)
		}
	}
	if _, err := queries.DeleteUserQuestionCheckpoint(ctx, turnID); err != nil {
		return fmt.Errorf("drop user question checkpoint: %w", err)
	}
	return nil
}

func invalidUserQuestionArgsResult(call ai.ToolCall, argsErr error) (string, aichattools.Result) {
	reason := strings.TrimSpace(argsErr.Error())
	if len(reason) > 500 {
		reason = reason[:500] + "\u2026[truncated]"
	}
	if reason == "" {
		reason = "the questions were not valid"
	}
	content := "tool \"ask_user_question\" call \"" + call.ID + " failed: " + reason + ", so nothing was asked. Send ONE call with 1..5 valid questions: each needs a nonblank id and prompt, an explicit single/multiple mode, an explicit allow_custom, and 2..8 options with unique ids."
	return "failed", aichattools.Result{Content: content, Summary: "invalid questions"}
}
