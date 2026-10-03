package aichatworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	// approvalTargetMax bounds the plaintext target line stored per approval.
	approvalTargetMax = 200
	// approvalAfterMax bounds the plaintext args summary stored per approval.
	approvalAfterMax = 500
	// defaultApprovalWait bounds one in-process wait for a decision. Nothing is
	// persisted to resume from, so a wait past this bound denies the call and
	// lets the model tell the user nobody answered. It stays under the turn
	// timeout, which still bounds the whole run.
	defaultApprovalWait = 2 * time.Minute
	// approvalPollInterval is how often a waiting turn re-reads its approval
	// row and refreshes the lease that keeps the turn claimed.
	approvalPollInterval = 250 * time.Millisecond
	// approvalTimeoutSummary records why a wait ended without an answer.
	approvalTimeoutSummary = "no decision before the approval wait ended; the call was not performed"
)

// mcpCallSummary is the bounded plaintext proposal stored per approval.
// Target/Before/After are untrusted display data, never secrets.
type mcpCallSummary struct {
	target string
	before string
	after  string
}

// proposalSummary maps one helper proposal to the stored summary, keeping the
// helper's own bounds and truncating defensively on top.
func proposalSummary(proposal aichattools.MCPApprovalProposal) mcpCallSummary {
	return mcpCallSummary{
		target: truncateRunes(proposal.Target, approvalTargetMax),
		before: truncateRunes(proposal.Before, approvalAfterMax),
		after:  truncateRunes(proposal.After, approvalAfterMax),
	}
}

// truncateRunes bounds display text on rune boundaries.
func truncateRunes(value string, max int) string {
	if len(value) <= max {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// canonicalArgsEqual compares proposed (immutable, from jsonb) with live call
// args semantically, so jsonb normalization can never read as a mismatch.
func canonicalArgsEqual(proposed []byte, live string) bool {
	var left, right any
	if err := json.Unmarshal(proposed, &left); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(live), &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// snapshotsEqualCanonical compares the snapshot taken when the approval was
// requested with a fresh helper snapshot semantically. Both empty means no
// pre-write state existed: the connection guard plus the immutable in-memory
// args carry the check instead. Any other difference blocks the write.
func snapshotsEqualCanonical(stored, fresh []byte) bool {
	blank := func(raw []byte) bool {
		trimmed := bytes.TrimSpace(raw)
		return len(trimmed) == 0 || string(trimmed) == "{}" || string(trimmed) == "null"
	}
	if blank(stored) {
		return blank(fresh)
	}
	return canonicalArgsEqual(stored, string(fresh))
}

// rowQuerier is the read side shared by a pool and an open transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// requestMCPApproval persists the pending approval and its approval_required
// event atomically: the event carries the row the frontend renders, so it must
// never exist without it. The turn stays running and keeps its lease. The
// stored row binds the exact connection, remote tool, immutable args, schema
// digest, and reviewed snapshot the worker executes on approval.
func (w *Worker) requestMCPApproval(ctx context.Context, claimed turn, call ai.ToolCall, handle *mcpTurnHandle, remote, schemaDigest string, proposal aichattools.MCPApprovalProposal, summary mcpCallSummary) (pgtype.UUID, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	created, err := sqlc.New(tx).CreateMCPApproval(ctx, sqlc.CreateMCPApprovalParams{
		TurnID:             claimed.ID,
		ToolCallID:         call.ID,
		ToolName:           call.Name,
		Provider:           handle.service,
		Service:            handle.service,
		ConnectionID:       handle.connectionID,
		ConnectionName:     handle.connectionName,
		RemoteToolName:     remote,
		SchemaDigest:       schemaDigest,
		Target:             summary.target,
		BeforeText:         summary.before,
		AfterText:          summary.after,
		Snapshot:           defaultEmptyJSON(proposal.Snapshot),
		ProposedArgs:       []byte(call.Args),
		ConnectionRevision: textUUID(handle.revision),
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	payload, err := w.approvalPayload(ctx, tx, created.ID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'approval_required', $2::jsonb)`, claimed.ID, payload); err != nil {
		return pgtype.UUID{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pgtype.UUID{}, err
	}
	return created.ID, nil
}

// approvalWait is how one in-process wait for a decision ended.
type approvalWait int

const (
	// approvalApproved runs the gated call after the live checks pass.
	approvalApproved approvalWait = iota
	// approvalDenied writes a denied tool result and continues the round.
	approvalDenied
	// approvalCancelled ends the turn because the user hit stop.
	approvalCancelled
	// approvalTimedOut denies the call because nobody decided in time.
	approvalTimedOut
	// approvalLost aborts the turn: the lease is gone or the database is
	// unreachable, so recovery owns the turn.
	approvalLost
)

// waitForApprovalDecision holds the turn in place until the user decides. The
// lease is refreshed on every tick so no second worker can claim the turn,
// and the same refresh reads cancel_requested_at so stop stays live during
// the wait. The status read comes first: cancelling invalidates the pending
// approval in one transaction, so seeing the invalidated status already
// means the cancel is committed and the lease refresh reports it.
func (w *Worker) waitForApprovalDecision(ctx context.Context, claimed turn, approvalID pgtype.UUID) (approvalWait, error) {
	deadline := time.Now().Add(w.approvalWait)
	for {
		var status string
		if err := w.pool.QueryRow(ctx, `SELECT status FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(&status); err != nil {
			return approvalLost, err
		}
		cancelled, err := w.refreshLease(ctx, claimed)
		if err != nil {
			return approvalLost, err
		}
		if cancelled {
			return approvalCancelled, nil
		}
		switch status {
		case "pending":
		case "approved":
			return approvalApproved, nil
		default:
			return approvalDenied, nil
		}
		if !time.Now().Before(deadline) {
			return approvalTimedOut, nil
		}
		if sleep(ctx, approvalPollInterval) != nil {
			return approvalLost, ctx.Err()
		}
	}
}

// closeApproval records a terminal state the user never chose (a wait that
// ended unanswered, or a target that moved) so the approval leaves the
// pending state the frontend renders.
func (w *Worker) closeApproval(ctx context.Context, claimed turn, approvalID pgtype.UUID, status, summary string) {
	if _, err := w.pool.Exec(ctx, `UPDATE ai_mcp_approvals SET status = $2, decided_at = now(), summary = $3, updated_at = now() WHERE id = $1 AND status IN ('pending', 'approved')`, approvalID, status, summary); err != nil {
		return
	}
	payload, err := w.approvalPayload(ctx, w.pool, approvalID)
	if err != nil {
		return
	}
	_ = w.event(ctx, claimed, "approval_decided", json.RawMessage(payload))
}

// defaultEmptyJSON maps an absent helper snapshot to the JSONB default so the
// NOT NULL column never sees NULL.
func defaultEmptyJSON(raw json.RawMessage) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("{}")
	}
	return raw
}

// approvalPayload renders the approval entity shape the SSE approval events
// carry, read back from the stored row.
func (w *Worker) approvalPayload(ctx context.Context, queries rowQuerier, approvalID pgtype.UUID) (string, error) {
	var row sqlc.GetMCPApprovalByIDRow
	if err := queries.QueryRow(ctx, `SELECT id, turn_id, tool_call_id, tool_name, provider, service, connection_id, connection_name, remote_tool_name, schema_digest, target, before_text, after_text, snapshot, proposed_args, connection_revision, status, created_at, decided_at, decided_by, summary FROM ai_mcp_approvals WHERE id = $1`, approvalID).Scan(
		&row.ID, &row.TurnID, &row.ToolCallID, &row.ToolName, &row.Provider, &row.Service, &row.ConnectionID, &row.ConnectionName, &row.RemoteToolName, &row.SchemaDigest, &row.Target, &row.BeforeText, &row.AfterText, &row.Snapshot, &row.ProposedArgs, &row.ConnectionRevision, &row.Status, &row.CreatedAt, &row.DecidedAt, &row.DecidedBy, &row.Summary,
	); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(map[string]any{"approval": approvalJSONFromWorker(row)})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// textUUID parses a UUID string for sqlc uuid params; empty means NULL.
func textUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if value == "" {
		return id
	}
	_ = id.Scan(value)
	return id
}

// approvalJSONFromWorker maps one approval row to the contract entity shape
// for SSE payloads (worker side; the app owns the HTTP shape).
func approvalJSONFromWorker(row sqlc.GetMCPApprovalByIDRow) map[string]any {
	approval := map[string]any{
		"id": row.ID.String(), "turn_id": row.TurnID.String(),
		"tool_call_id": row.ToolCallID, "tool_name": row.ToolName,
		"target": row.Target, "before": row.BeforeText, "after": row.AfterText,
		"status": row.Status, "created_at": row.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"proposed_args": json.RawMessage(append([]byte(nil), row.ProposedArgs...)),
	}
	if row.Provider != "" {
		approval["provider"] = row.Provider
	}
	if row.Service != "" {
		approval["service"] = row.Service
	}
	if row.ConnectionID.Valid {
		approval["connection_id"] = row.ConnectionID.String()
	}
	if row.ConnectionName != "" {
		approval["connection_name"] = row.ConnectionName
	}
	if row.RemoteToolName != "" {
		approval["remote_tool_name"] = row.RemoteToolName
	}
	if row.DecidedAt.Valid {
		approval["decided_at"] = row.DecidedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if row.Summary != "" {
		approval["summary"] = row.Summary
	}
	return approval
}

// approvedMCPCall is an approved Ask call awaiting its live rechecks.
type approvedMCPCall struct {
	handle       *mcpTurnHandle
	remote       string
	proposal     aichattools.MCPApprovalProposal
	schemaDigest string
	call         ai.ToolCall
}

// approvedCallUnchanged re-checks an approved call against the current
// saved rows and the live session before dispatch: membership, feature, and
// revision must still pass, the exact tool must still be discovered with the
// approved schema digest, the rule must not be Deny, the stored args must
// match the in-memory ones, and a fresh helper run through the
// policy-enforcing preflight session must reproduce the snapshot taken when
// the approval was requested.
func (w *Worker) approvedCallUnchanged(ctx context.Context, scope turnScope, approved approvedMCPCall, storedArgs []byte) bool {
	handle := approved.handle
	if handle == nil {
		return false
	}
	permission, currentDigest, err := w.verifyMCPCallCurrent(ctx, scope, handle.connectionID, handle.revision, approved.remote)
	if err != nil || permission == "deny" || currentDigest != approved.schemaDigest {
		return false
	}
	if !canonicalArgsEqual(storedArgs, approved.call.Args) {
		return false
	}
	liveDigest, ok := liveMCPToolDigest(handle.session, approved.remote)
	if !ok || liveDigest != currentDigest {
		return false
	}
	fresh, err := aichattools.PrepareMCPApproval(ctx, w.preflightSessionFor(scope, handle), handle.service, approved.remote, json.RawMessage(approved.call.Args))
	if err != nil {
		return false
	}
	return snapshotsEqualCanonical(approved.proposal.Snapshot, fresh.Snapshot)
}

// denyToolCall records a refused call as a paired denied tool result. It
// never executes the remote call.
func (w *Worker) denyToolCall(ctx context.Context, claimed turn, call ai.ToolCall, live *[]ai.Message) error {
	queries := sqlc.New(w.pool)
	content := call.Name + " error: the requested MCP change was not approved and was not performed."
	rowID, _, err := ensureToolCallRow(ctx, w.pool, queries, claimed.ID, call)
	if err != nil {
		return err
	}
	if err := markToolCallRunning(ctx, w.pool, rowID); err != nil {
		return err
	}
	if err := queries.CompleteAIToolCall(ctx, sqlc.CompleteAIToolCallParams{
		ID: rowID, Status: "failed", ResultContent: content, Summary: "not approved",
	}); err != nil {
		return err
	}
	if err := w.event(ctx, claimed, "tool_result", map[string]string{"id": call.ID, "name": call.Name, "summary": "not approved", "status": "failed"}); err != nil {
		return err
	}
	*live = append(*live, ai.Message{Role: ai.RoleTool, Content: content, ToolCallID: call.ID, Name: call.Name})
	return nil
}

// ensureToolCallRow returns the durable row for one call, inserting an awaiting
// row when none exists yet. isNew tells the caller whether to emit the
// tool_call event (exactly once per row).
func ensureToolCallRow(ctx context.Context, pool *pgxpool.Pool, queries *sqlc.Queries, turnID pgtype.UUID, call ai.ToolCall) (pgtype.UUID, bool, error) {
	var id pgtype.UUID
	var status string
	err := pool.QueryRow(ctx, `SELECT id, status FROM ai_tool_calls WHERE turn_id = $1 AND call_id = $2`, turnID, call.ID).Scan(&id, &status)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, false, err
	}
	var nextSeq int32
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq), -1) + 1 FROM ai_tool_calls WHERE turn_id = $1`, turnID).Scan(&nextSeq); err != nil {
		return pgtype.UUID{}, false, err
	}
	inserted, err := queries.InsertAIToolCall(ctx, sqlc.InsertAIToolCallParams{
		TurnID: turnID, Seq: nextSeq, CallID: call.ID, Name: call.Name,
		Args: []byte(call.Args), Status: "awaiting",
	})
	if err != nil {
		return pgtype.UUID{}, false, err
	}
	return inserted, true, nil
}

// markToolCallRunning moves one durable row to running before the network.
func markToolCallRunning(ctx context.Context, pool *pgxpool.Pool, rowID pgtype.UUID) error {
	_, err := pool.Exec(ctx, `UPDATE ai_tool_calls SET status = 'running', updated_at = now() WHERE id = $1`, rowID)
	return err
}
