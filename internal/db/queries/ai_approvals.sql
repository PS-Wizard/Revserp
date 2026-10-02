-- name: CreateCMSApproval :one
INSERT INTO ai_cms_approvals (
    turn_id,
    tool_call_id,
    tool_name,
    provider,
    target,
    before_text,
    after_text,
    snapshot,
    proposed_args,
    connection_revision,
    status
) VALUES (
    sqlc.arg(turn_id),
    sqlc.arg(tool_call_id),
    sqlc.arg(tool_name),
    sqlc.arg(provider),
    sqlc.arg(target),
    sqlc.arg(before_text),
    sqlc.arg(after_text),
    sqlc.arg(snapshot)::jsonb,
    sqlc.arg(proposed_args)::jsonb,
    sqlc.arg(connection_revision)::uuid,
    'pending'
)
RETURNING id, turn_id, tool_call_id, tool_name, provider, target, before_text, after_text, snapshot, proposed_args, connection_revision, status, created_at, decided_at, decided_by, summary;

-- name: ListCMSApprovalsForConversation :many
SELECT a.id, a.turn_id, a.tool_call_id, a.tool_name, a.provider, a.target, a.before_text, a.after_text, a.snapshot, a.proposed_args, a.connection_revision, a.status, a.created_at, a.decided_at, a.decided_by, a.summary
FROM ai_cms_approvals AS a
INNER JOIN ai_turns AS t ON t.id = a.turn_id
INNER JOIN ai_conversations AS c ON c.id = t.conversation_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
WHERE c.id = sqlc.arg(conversation_id)
ORDER BY a.created_at ASC, a.id ASC;

-- name: ListCMSApprovalsForTurn :many
SELECT id, turn_id, tool_call_id, tool_name, provider, target, before_text, after_text, snapshot, proposed_args, connection_revision, status, created_at, decided_at, decided_by, summary
FROM ai_cms_approvals
WHERE turn_id = sqlc.arg(turn_id)
ORDER BY created_at ASC, id ASC;

-- name: LockCMSApprovalForDecision :one
SELECT a.id, a.turn_id, a.tool_call_id, a.tool_name, a.provider, a.target, a.before_text, a.after_text, a.snapshot, a.proposed_args, a.connection_revision, a.status, a.created_at, a.decided_at, a.decided_by, a.summary,
    t.conversation_id, t.status AS turn_status, t.created_by_user_id
FROM ai_cms_approvals AS a
INNER JOIN ai_turns AS t ON t.id = a.turn_id
INNER JOIN ai_conversations AS c ON c.id = t.conversation_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
WHERE a.id = sqlc.arg(approval_id)
  AND c.id = sqlc.arg(conversation_id)
FOR UPDATE OF a
FOR KEY SHARE OF om;

-- name: DecideCMSApproval :one
UPDATE ai_cms_approvals
SET status = sqlc.arg(status),
    decided_at = now(),
    decided_by = sqlc.arg(decided_by),
    summary = sqlc.arg(summary),
    updated_at = now()
WHERE id = sqlc.arg(approval_id) AND status = 'pending'
RETURNING id, turn_id, tool_call_id, tool_name, provider, target, before_text, after_text, snapshot, proposed_args, connection_revision, status, created_at, decided_at, decided_by, summary;

-- name: GetCMSApprovalByID :one
SELECT id, turn_id, tool_call_id, tool_name, provider, target, before_text, after_text, snapshot, proposed_args, connection_revision, status, created_at, decided_at, decided_by, summary
FROM ai_cms_approvals
WHERE id = sqlc.arg(approval_id);

-- name: MarkCMSApprovalExecuting :execrows
UPDATE ai_cms_approvals
SET status = 'executing',
    updated_at = now()
WHERE id = sqlc.arg(approval_id) AND status = 'approved';

-- name: CompleteCMSApproval :execrows
UPDATE ai_cms_approvals
SET status = sqlc.arg(status),
    summary = sqlc.arg(summary),
    updated_at = now()
WHERE id = sqlc.arg(approval_id) AND status = 'executing';

-- name: InvalidatePendingCMSApprovalsForTurn :many
UPDATE ai_cms_approvals
SET status = 'invalidated',
    decided_at = now(),
    summary = sqlc.arg(summary),
    updated_at = now()
WHERE turn_id = sqlc.arg(turn_id) AND status = 'pending'
RETURNING id, turn_id, tool_call_id, tool_name, provider, target, before_text, after_text, snapshot, proposed_args, connection_revision, status, created_at, decided_at, decided_by, summary;

-- name: InvalidatePendingCMSApprovalsForProject :many
UPDATE ai_cms_approvals AS a
SET status = 'invalidated',
    decided_at = now(),
    summary = sqlc.arg(summary),
    updated_at = now()
FROM ai_turns AS t
INNER JOIN ai_conversations AS c ON c.id = t.conversation_id
WHERE a.turn_id = t.id
  AND c.project_id = sqlc.arg(project_id)
  AND a.status = 'pending'
RETURNING a.id, a.turn_id, a.tool_call_id, a.tool_name, a.provider, a.target, a.before_text, a.after_text, a.snapshot, a.proposed_args, a.connection_revision, a.status, a.created_at, a.decided_at, a.decided_by, a.summary;

-- name: FailExecutingCMSApprovalsForTurn :many
UPDATE ai_cms_approvals
SET status = 'failed',
    decided_at = now(),
    summary = 'cms write outcome unknown: the worker stopped while the approved call was executing. The remote call may have applied. It was not retried; inspect the outcome with a read tool instead.',
    updated_at = now()
WHERE turn_id = sqlc.arg(turn_id) AND status = 'executing'
RETURNING id, turn_id, tool_call_id, tool_name, provider, status;

-- name: InsertAITurnEvent :exec
INSERT INTO ai_turn_events(turn_id, event_type, payload)
VALUES (sqlc.arg(turn_id), sqlc.arg(event_type), sqlc.arg(payload)::jsonb);

-- name: StopWaitingAITurn :execrows
UPDATE ai_turns
SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
    status = 'stopped',
    error_code = 'cancelled',
    completed_at = now(),
    claimed_by = NULL,
    lease_expires_at = NULL,
    heartbeat_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(turn_id) AND status = 'waiting_for_user';

-- name: FailWaitingTurnsForProject :many
UPDATE ai_turns AS t
SET status = 'failed',
    error_code = 'cms_connection_changed',
    completed_at = now(),
    claimed_by = NULL,
    lease_expires_at = NULL,
    heartbeat_at = NULL,
    updated_at = now()
FROM ai_conversations AS c
WHERE t.conversation_id = c.id
  AND c.project_id = sqlc.arg(project_id)
  AND t.status = 'waiting_for_user'
RETURNING t.id, t.conversation_id, (t.output_started_at IS NOT NULL)::boolean AS is_partial;

-- name: FailUnknownAIToolCallsForTurn :many
UPDATE ai_tool_calls
SET status = 'failed',
    result_content = 'cms write outcome unknown: the worker stopped while the approved call was executing. The remote call may have applied. It was not retried; inspect the outcome with a read tool instead.',
    summary = 'cms write outcome unknown, do not retry',
    updated_at = now()
WHERE turn_id = sqlc.arg(turn_id) AND status = 'running'
RETURNING call_id, name;
