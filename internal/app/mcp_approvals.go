package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// mcpApprovalJSON is the durable approval entity. Target/Before/After are
// plain untrusted display text (never HTML-rendered); proposed_args carries
// the immutable original call the worker executes on approval. tool_name is
// the model alias; connection_id/connection_name/remote_tool_name identify
// the exact connection and remote tool. service replaces provider for new
// records; provider stays for historical rows.
type mcpApprovalJSON struct {
	ID             string          `json:"id"`
	TurnID         string          `json:"turn_id"`
	ToolCallID     string          `json:"tool_call_id"`
	ToolName       string          `json:"tool_name"`
	Provider       string          `json:"provider,omitempty"`
	Service        string          `json:"service,omitempty"`
	ConnectionID   *string         `json:"connection_id,omitempty"`
	ConnectionName *string         `json:"connection_name,omitempty"`
	RemoteToolName *string         `json:"remote_tool_name,omitempty"`
	Target         string          `json:"target"`
	Before         string          `json:"before"`
	After          string          `json:"after"`
	Status         string          `json:"status"`
	CreatedAt      string          `json:"created_at"`
	DecidedAt      *string         `json:"decided_at,omitempty"`
	Summary        *string         `json:"summary,omitempty"`
	ProposedArgs   json.RawMessage `json:"proposed_args,omitempty"`
}

// mcpApprovalData mirrors the approval columns every approval query returns,
// so one builder serves list, decision, and invalidation rows.
type mcpApprovalData struct {
	ID                 pgtype.UUID
	TurnID             pgtype.UUID
	ToolCallID         string
	ToolName           string
	Provider           string
	Service            string
	ConnectionID       pgtype.UUID
	ConnectionName     string
	RemoteToolName     string
	SchemaDigest       string
	Target             string
	BeforeText         string
	AfterText          string
	Snapshot           []byte
	ProposedArgs       []byte
	ConnectionRevision pgtype.UUID
	Status             string
	CreatedAt          pgtype.Timestamptz
	DecidedAt          pgtype.Timestamptz
	DecidedBy          pgtype.UUID
	Summary            string
}

// newMCPApprovalJSON maps one approval row to the contract entity shape.
// Times render RFC3339; empty summary/decided fields stay absent.
func newMCPApprovalJSON(data mcpApprovalData) mcpApprovalJSON {
	approval := mcpApprovalJSON{
		ID:         data.ID.String(),
		TurnID:     data.TurnID.String(),
		ToolCallID: data.ToolCallID,
		ToolName:   data.ToolName,
		Target:     data.Target,
		Before:     data.BeforeText,
		After:      data.AfterText,
		Status:     data.Status,
		CreatedAt:  data.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
	if data.Provider != "" {
		approval.Provider = data.Provider
	}
	if data.Service != "" {
		approval.Service = data.Service
	}
	if data.ConnectionID.Valid {
		id := data.ConnectionID.String()
		approval.ConnectionID = &id
	}
	if data.ConnectionName != "" {
		name := data.ConnectionName
		approval.ConnectionName = &name
	}
	if data.RemoteToolName != "" {
		remote := data.RemoteToolName
		approval.RemoteToolName = &remote
	}
	if data.DecidedAt.Valid {
		decided := data.DecidedAt.Time.UTC().Format(time.RFC3339)
		approval.DecidedAt = &decided
	}
	if data.Summary != "" {
		summary := data.Summary
		approval.Summary = &summary
	}
	if len(data.ProposedArgs) > 0 {
		approval.ProposedArgs = json.RawMessage(append([]byte(nil), data.ProposedArgs...))
	}
	return approval
}

func mcpApprovalDecidedPayloadFromData(data mcpApprovalData) (map[string]any, error) {
	approval := newMCPApprovalJSON(data)
	encoded, err := json.Marshal(approval)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return map[string]any{"approval": decoded}, nil
}

func mcpApprovalDataFromList(row sqlc.ListMCPApprovalsForConversationRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromDecided(row sqlc.DecideMCPApprovalRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromGot(row sqlc.GetMCPApprovalByIDRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromInvalidatedTurn(row sqlc.InvalidatePendingMCPApprovalsForTurnRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromInvalidatedConnection(row sqlc.InvalidatePendingMCPApprovalsForConnectionRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromInvalidatedConnectionTool(row sqlc.InvalidatePendingMCPApprovalsForConnectionToolRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromTurnRow(row sqlc.ListMCPApprovalsForTurnRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func mcpApprovalDataFromLocked(row sqlc.LockMCPApprovalForDecisionRow) mcpApprovalData {
	return mcpApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Service: row.Service, ConnectionID: row.ConnectionID,
		ConnectionName: row.ConnectionName, RemoteToolName: row.RemoteToolName, SchemaDigest: row.SchemaDigest,
		Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func (a *App) handleListMCPApprovals(w http.ResponseWriter, r *http.Request) {
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
	rows, err := a.Queries.ListMCPApprovalsForConversation(r.Context(), sqlc.ListMCPApprovalsForConversationParams{
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	approvals := make([]mcpApprovalJSON, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, newMCPApprovalJSON(mcpApprovalDataFromList(row)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

type mcpApprovalDecisionRequest struct {
	Decision    string `json:"decision"`
	AlwaysAllow *bool  `json:"always_allow,omitempty"`
}

// handleDecideMCPApproval applies one approve/reject decision. The decision
// body carries no args: the worker always executes the immutable
// proposed_args stored when it asked. The waiting worker polls the row and
// continues its own run, so nothing is queued here. Compare-and-set on
// pending keeps concurrent decisions safe; repeating the same outcome is
// idempotent, any other repeat conflicts.
//
// always_allow is valid only with approve: it saves the exact
// connection/tool allow rule and approves the exact pending request in one
// transaction. The rule covers only that connection and that remote tool.
func (a *App) handleDecideMCPApproval(w http.ResponseWriter, r *http.Request) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	approvalID, err := parseUUIDParam(chi.URLParam(r, "approvalID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid approval id")
		return
	}
	var body mcpApprovalDecisionRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	var status string
	switch body.Decision {
	case "approve":
		status = "approved"
	case "reject":
		status = "rejected"
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid decision")
		return
	}
	alwaysAllow := body.AlwaysAllow != nil && *body.AlwaysAllow
	if alwaysAllow && status != "approved" {
		writeJSONError(w, http.StatusBadRequest, "always_allow is valid only with approve")
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

	// Membership plus a serialization lock shared with turn submission.
	if _, err := queries.LockAIConversationForTurn(r.Context(), sqlc.LockAIConversationForTurnParams{
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
	// Lock order for the whole marketplace write path is the conversation,
	// then the connection row, then approval rows, then permission rows, so a
	// concurrent permission edit serializes instead of deadlocking. The
	// scoped read below carries no lock, so the connection row is locked
	// before the approval row.
	pre, err := queries.GetMCPApprovalForDecision(r.Context(), sqlc.GetMCPApprovalForDecisionParams{
		ApprovalID:     approvalID,
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "approval not found")
			return
		}
		serverError(w, r, err)
		return
	}
	// Only the turn initiator may decide; fail closed (no org-owner override).
	// This runs before the idempotent replay below, so a non-initiator can
	// never observe or re-trigger someone else's decision.
	if pre.CreatedByUserID != principal.User.ID {
		writeJSONError(w, http.StatusForbidden, "only the turn initiator can decide")
		return
	}
	if pre.Status != "pending" {
		// Repeated decision: same outcome replays the current approval,
		// any other outcome conflicts. Never requeues. A repeated
		// always_allow replays too: it must not resurrect a rule revoked
		// after the original success, so no permission row is written here.
		current, err := queries.GetMCPApprovalByID(r.Context(), approvalID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if current.Status != status {
			writeJSONError(w, http.StatusConflict, "approval already decided")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"approval": newMCPApprovalJSON(mcpApprovalDataFromGot(current)),
			"turn_id":  current.TurnID.String(),
		})
		return
	}
	// An approve touches the connection row, so it is locked before the
	// approval row. A removed connection fails the approve; rejecting needs
	// no connection and skips this lock.
	var connOrg pgtype.UUID
	if status == "approved" && pre.ConnectionID.Valid {
		connRow, err := queries.LockMCPConnectionForDecision(r.Context(), sqlc.LockMCPConnectionForDecisionParams{
			ConnectionID:   pre.ConnectionID,
			ConversationID: conversationID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusConflict, "connection no longer exists")
				return
			}
			serverError(w, r, err)
			return
		}
		connOrg = connRow.OrganizationID
	}
	locked, err := queries.LockMCPApprovalForDecision(r.Context(), sqlc.LockMCPApprovalForDecisionParams{
		ApprovalID:     approvalID,
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "approval already decided")
			return
		}
		serverError(w, r, err)
		return
	}
	if locked.Status != "pending" {
		// Lost the race with another decider after the scoped read.
		current, err := queries.GetMCPApprovalByID(r.Context(), approvalID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if current.Status != status {
			writeJSONError(w, http.StatusConflict, "approval already decided")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"approval": newMCPApprovalJSON(mcpApprovalDataFromGot(current)),
			"turn_id":  current.TurnID.String(),
		})
		return
	}
	// The waiting worker holds the turn in running state; waiting_for_user
	// only covers turns paused by an older worker build.
	if locked.TurnStatus != "running" && locked.TurnStatus != "waiting_for_user" {
		writeJSONError(w, http.StatusConflict, "turn is no longer waiting for approval")
		return
	}
	if status == "approved" {
		// An approve dispatches the exact saved call, so the connection
		// revision and the exact tool's current schema must still hold: a
		// /check or a replacement after the card was rendered blocks the
		// stale decision. A current Deny blocks a plain approve, while an
		// explicit always_allow on the fresh card re-grants the exact rule.
		// Rejecting needs no such freshness: denying a stale call is
		// always safe.
		state, err := a.checkMCPApprovalCurrent(r.Context(), queries, principal.User.ID, locked)
		if err != nil {
			writeJSONError(w, http.StatusConflict, "approval is no longer valid for its connection")
			return
		}
		if state.Permission == "deny" && !alwaysAllow {
			writeJSONError(w, http.StatusConflict, "tool is denied on its connection")
			return
		}
	}
	if alwaysAllow {
		// Saving a project-wide rule is a permission write, so it keeps
		// the connection-owner boundary every other permission write
		// requires: the initiator alone cannot grant it. The freshness
		// check above already ran, so a stale card can neither execute nor
		// regrant a revoked rule, and a platform-restricted tool can never
		// be made runnable through Always allow. Plain approve/reject stay
		// initiator-only.
		if !connOrg.Valid {
			writeJSONError(w, http.StatusConflict, "approval has no connection to allow")
			return
		}
		if err := requireOrganizationOwner(r.Context(), queries, connOrg, principal.User.ID); err != nil {
			writeInvitePermissionError(w, err)
			return
		}
		if reason, restricted := mcpToolRestrictionReason(locked.Service, locked.RemoteToolName); restricted {
			writeJSONError(w, http.StatusBadRequest, "tool is unavailable: "+reason)
			return
		}
	}
	summary := "approved by turn initiator"
	if status == "rejected" {
		summary = "rejected by turn initiator; the call was not executed"
	} else if alwaysAllow {
		summary = "approved by turn initiator with always allow for this connection and tool"
	}
	// The compare-and-set wins the race with a concurrent permission edit:
	// a revoked pending row decides nothing here. The rule write below runs
	// only after this CAS succeeded, so a lost race can never resurrect a
	// revoked call into a fresh rule.
	decided, err := queries.DecideMCPApproval(r.Context(), sqlc.DecideMCPApprovalParams{
		ApprovalID: approvalID,
		Status:     status,
		DecidedBy:  principal.User.ID,
		Summary:    summary,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "approval already decided")
			return
		}
		serverError(w, r, err)
		return
	}
	if alwaysAllow {
		if err := queries.UpsertMCPToolPermission(r.Context(), sqlc.UpsertMCPToolPermissionParams{
			ConnectionID: locked.ConnectionID,
			ToolName:     locked.RemoteToolName,
			Permission:   "allow",
		}); err != nil {
			serverError(w, r, err)
			return
		}
	}
	approval := newMCPApprovalJSON(mcpApprovalDataFromDecided(decided))
	payload, err := json.Marshal(map[string]any{"approval": approval})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'approval_decided', $2::jsonb)`, locked.TurnID, string(payload)); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"approval": approval,
		"turn_id":  locked.TurnID.String(),
	})
}

// mcpDecisionState is the current saved state one approve validates
// against: the connection revision, the discovered tools, and the deciding
// user's effective rule for the exact tool.
type mcpDecisionState struct {
	Revision   string
	Tools      []byte
	Permission string
}

// checkMCPApprovalCurrent validates an approve against the current saved
// rows: the connection still exists under the approval's conversation
// project with an unchanged revision, and the exact remote tool is still
// discovered with the approved schema digest. It reads stored rows only and
// never crosses the network inside the decision transaction. The approval
// row lock plus the permission-edit invalidation locking the same rows
// serialize a concurrent Deny: whichever commits first wins, and the loser
// sees no pending row.
func (a *App) checkMCPApprovalCurrent(ctx context.Context, queries *sqlc.Queries, userID pgtype.UUID, locked sqlc.LockMCPApprovalForDecisionRow) (mcpDecisionState, error) {
	if !locked.ConnectionID.Valid || locked.RemoteToolName == "" || !locked.ConnectionRevision.Valid {
		return mcpDecisionState{}, pgx.ErrNoRows
	}
	state, err := queries.GetMCPDecisionCheck(ctx, sqlc.GetMCPDecisionCheckParams{
		UserID:         userID,
		ToolName:       locked.RemoteToolName,
		ConnectionID:   locked.ConnectionID,
		ConversationID: locked.ConversationID,
	})
	if err != nil {
		return mcpDecisionState{}, err
	}
	if state.Revision != locked.ConnectionRevision.String() {
		return mcpDecisionState{}, pgx.ErrNoRows
	}
	stored, err := parseMCPStoredTools(state.Tools)
	if err != nil {
		return mcpDecisionState{}, err
	}
	for _, tool := range stored {
		if tool.Name != locked.RemoteToolName {
			continue
		}
		if locked.SchemaDigest == "" {
			return mcpDecisionState{Revision: state.Revision, Tools: state.Tools, Permission: state.Permission}, nil
		}
		digest, err := mcpCanonicalSchemaDigest(tool.InputSchema)
		if err != nil || digest != locked.SchemaDigest {
			return mcpDecisionState{}, pgx.ErrNoRows
		}
		return mcpDecisionState{Revision: state.Revision, Tools: state.Tools, Permission: state.Permission}, nil
	}
	return mcpDecisionState{}, pgx.ErrNoRows
}

// mcpCanonicalSchemaDigest hashes the full canonical schema, the same
// algorithm the worker uses, so approval-time and decision-time digests
// compare exactly.
func mcpCanonicalSchemaDigest(schema json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(schema))
	if trimmed == "" {
		trimmed = "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
