package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// cmsApprovalJSON is the durable approval entity. Target/Before/After are
// plain untrusted display text (never HTML-rendered); proposed_args carries
// the immutable original call the worker executes on approval.
type cmsApprovalJSON struct {
	ID           string          `json:"id"`
	TurnID       string          `json:"turn_id"`
	ToolCallID   string          `json:"tool_call_id"`
	ToolName     string          `json:"tool_name"`
	Provider     string          `json:"provider"`
	Target       string          `json:"target"`
	Before       string          `json:"before"`
	After        string          `json:"after"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"created_at"`
	DecidedAt    *string         `json:"decided_at,omitempty"`
	Summary      *string         `json:"summary,omitempty"`
	ProposedArgs json.RawMessage `json:"proposed_args,omitempty"`
}

// cmsApprovalData mirrors the approval columns every approval query returns,
// so one builder serves list, decision, and invalidation rows.
type cmsApprovalData struct {
	ID                 pgtype.UUID
	TurnID             pgtype.UUID
	ToolCallID         string
	ToolName           string
	Provider           string
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

// newCMSApprovalJSON maps one approval row to the contract entity shape.
// Times render RFC3339; empty summary/decided fields stay absent.
func newCMSApprovalJSON(data cmsApprovalData) cmsApprovalJSON {
	approval := cmsApprovalJSON{
		ID:         data.ID.String(),
		TurnID:     data.TurnID.String(),
		ToolCallID: data.ToolCallID,
		ToolName:   data.ToolName,
		Provider:   data.Provider,
		Target:     data.Target,
		Before:     data.BeforeText,
		After:      data.AfterText,
		Status:     data.Status,
		CreatedAt:  data.CreatedAt.Time.UTC().Format(time.RFC3339),
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

// cmsApprovalDecidedPayload builds the approval_decided SSE payload for an
// invalidated approval row (project replace/disconnect path).
func cmsApprovalDecidedPayload(row sqlc.InvalidatePendingCMSApprovalsForProjectRow) (map[string]any, error) {
	approval := newCMSApprovalJSON(cmsApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	})
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

func cmsApprovalDataFromList(row sqlc.ListCMSApprovalsForConversationRow) cmsApprovalData {
	return cmsApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func cmsApprovalDataFromDecided(row sqlc.DecideCMSApprovalRow) cmsApprovalData {
	return cmsApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func cmsApprovalDataFromGot(row sqlc.GetCMSApprovalByIDRow) cmsApprovalData {
	return cmsApprovalData{
		ID: row.ID, TurnID: row.TurnID, ToolCallID: row.ToolCallID, ToolName: row.ToolName,
		Provider: row.Provider, Target: row.Target, BeforeText: row.BeforeText, AfterText: row.AfterText,
		Snapshot: row.Snapshot, ProposedArgs: row.ProposedArgs, ConnectionRevision: row.ConnectionRevision,
		Status: row.Status, CreatedAt: row.CreatedAt, DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy,
		Summary: row.Summary,
	}
}

func (a *App) handleListCMSApprovals(w http.ResponseWriter, r *http.Request) {
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
	rows, err := a.Queries.ListCMSApprovalsForConversation(r.Context(), sqlc.ListCMSApprovalsForConversationParams{
		ConversationID: conversationID,
		UserID:         principal.User.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	approvals := make([]cmsApprovalJSON, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, newCMSApprovalJSON(cmsApprovalDataFromList(row)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

type cmsApprovalDecisionRequest struct {
	Decision string `json:"decision"`
}

// handleDecideCMSApproval applies one approve/reject decision. The decision
// body carries no args: the worker always executes the immutable
// proposed_args stored when it asked. The waiting worker polls the row and
// continues its own run, so nothing is queued here. Compare-and-set on
// pending keeps concurrent decisions safe; repeating the same outcome is
// idempotent, any other repeat conflicts.
func (a *App) handleDecideCMSApproval(w http.ResponseWriter, r *http.Request) {
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
	var body cmsApprovalDecisionRequest
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
	locked, err := queries.LockCMSApprovalForDecision(r.Context(), sqlc.LockCMSApprovalForDecisionParams{
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
	if locked.Status != "pending" {
		// Repeated decision: same outcome replays the current approval,
		// any other outcome conflicts. Never requeues.
		current, err := queries.GetCMSApprovalByID(r.Context(), approvalID)
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
			"approval": newCMSApprovalJSON(cmsApprovalDataFromGot(current)),
			"turn_id":  current.TurnID.String(),
		})
		return
	}
	// Only the turn initiator may decide; fail closed (no org-owner override).
	if locked.CreatedByUserID != principal.User.ID {
		writeJSONError(w, http.StatusForbidden, "only the turn initiator can decide")
		return
	}
	// The waiting worker holds the turn in running state; waiting_for_user
	// only covers turns paused by an older worker build.
	if locked.TurnStatus != "running" && locked.TurnStatus != "waiting_for_user" {
		writeJSONError(w, http.StatusConflict, "turn is no longer waiting for approval")
		return
	}
	summary := "approved by turn initiator"
	if status == "rejected" {
		summary = "rejected by turn initiator; the call was not executed"
	}
	decided, err := queries.DecideCMSApproval(r.Context(), sqlc.DecideCMSApprovalParams{
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

	approval := newCMSApprovalJSON(cmsApprovalDataFromDecided(decided))
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
