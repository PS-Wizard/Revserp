-- to_regclass never fails, so this probe cannot abort the transaction.
-- name: UserQuestionTablesExist :one
SELECT to_regclass('public.ai_user_question_requests') IS NOT NULL AND to_regclass('public.ai_user_question_checkpoints') IS NOT NULL AS tables_exist;

-- name: CreateUserQuestionRequest :one
INSERT INTO ai_user_question_requests (
    turn_id,
    tool_call_id,
    questions,
    status
) VALUES (
    sqlc.arg(turn_id),
    sqlc.arg(tool_call_id),
    sqlc.arg(questions)::jsonb,
    'pending'
)
RETURNING id, turn_id, tool_call_id, questions, answers, reply, status, decided_by, created_at, answered_at;

-- name: GetUserQuestionRequestByID :one
SELECT id, turn_id, tool_call_id, questions, answers, reply, status, decided_by, created_at, answered_at
FROM ai_user_question_requests
WHERE id = sqlc.arg(id);

-- name: ListUserQuestionRequestsForConversation :many
SELECT q.id, q.turn_id, q.tool_call_id, q.questions, q.answers, q.reply, q.status, q.decided_by, q.created_at, q.answered_at
FROM ai_user_question_requests AS q
INNER JOIN ai_turns AS t ON t.id = q.turn_id
WHERE t.conversation_id = sqlc.arg(conversation_id)
ORDER BY q.created_at ASC, q.id ASC;

-- name: ListUserQuestionRequestsForTurn :many
SELECT id, turn_id, tool_call_id, questions, answers, reply, status, decided_by, created_at, answered_at
FROM ai_user_question_requests
WHERE turn_id = sqlc.arg(turn_id)
ORDER BY created_at ASC, id ASC;

-- name: GetUserQuestionRequestForDecision :one
SELECT q.id, q.turn_id, q.tool_call_id, q.questions, q.answers, q.reply, q.status, q.decided_by, q.created_at, q.answered_at,
    t.conversation_id, t.status AS turn_status, t.created_by_user_id
FROM ai_user_question_requests AS q
INNER JOIN ai_turns AS t ON t.id = q.turn_id
INNER JOIN ai_conversations AS c ON c.id = t.conversation_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
WHERE q.id = sqlc.arg(id)
  AND c.id = sqlc.arg(conversation_id);

-- name: LockUserQuestionRequestForDecision :one
SELECT q.id, q.turn_id, q.tool_call_id, q.questions, q.answers, q.reply, q.status, q.decided_by, q.created_at, q.answered_at,
    t.conversation_id, t.status AS turn_status, t.created_by_user_id
FROM ai_user_question_requests AS q
INNER JOIN ai_turns AS t ON t.id = q.turn_id
INNER JOIN ai_conversations AS c ON c.id = t.conversation_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
WHERE q.id = sqlc.arg(id)
  AND c.id = sqlc.arg(conversation_id)
FOR UPDATE OF q
FOR KEY SHARE OF om;

-- name: DecideUserQuestionRequest :one
UPDATE ai_user_question_requests
SET status = sqlc.arg(status),
    answers = sqlc.arg(answers)::jsonb,
    reply = sqlc.arg(reply),
    decided_by = sqlc.arg(decided_by),
    answered_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING id, turn_id, tool_call_id, questions, answers, reply, status, decided_by, created_at, answered_at;

-- name: CancelPendingUserQuestionsForTurn :many
UPDATE ai_user_question_requests
SET status = 'cancelled',
    decided_by = sqlc.arg(decided_by),
    answered_at = now(),
    updated_at = now()
WHERE turn_id = sqlc.arg(turn_id) AND status = 'pending'
RETURNING id, turn_id, tool_call_id, questions, answers, reply, status, decided_by, created_at, answered_at;

-- name: InsertUserQuestionCheckpoint :execrows
INSERT INTO ai_user_question_checkpoints (
    turn_id,
    question_request_id,
    messages,
    usage,
    budgets,
    rundown,
    deferred_calls,
    round,
    next_seq
) VALUES (
    sqlc.arg(turn_id),
    sqlc.arg(question_request_id),
    sqlc.arg(messages)::jsonb,
    sqlc.arg(usage)::jsonb,
    sqlc.arg(budgets)::jsonb,
    sqlc.arg(rundown)::jsonb,
    sqlc.arg(deferred_calls)::jsonb,
    sqlc.arg(round)::int,
    sqlc.arg(next_seq)::int
)
ON CONFLICT (turn_id) DO NOTHING;

-- name: GetUserQuestionCheckpoint :one
SELECT turn_id, question_request_id, messages, usage, budgets, rundown, deferred_calls, round, next_seq
FROM ai_user_question_checkpoints
WHERE turn_id = sqlc.arg(turn_id);

-- name: DeleteUserQuestionCheckpoint :execrows
DELETE FROM ai_user_question_checkpoints
WHERE turn_id = sqlc.arg(turn_id);

-- name: ParkRunningTurnForQuestions :execrows
UPDATE ai_turns
SET status = 'waiting_for_user',
    claimed_by = NULL,
    lease_expires_at = NULL,
    heartbeat_at = NULL,
    output_started_at = COALESCE(output_started_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(turn_id)
  AND status = 'running'
  AND claimed_by = sqlc.arg(claimed_by)
  AND lease_expires_at > now()
  AND cancel_requested_at IS NULL;

-- name: QueueWaitingTurnForQuestions :execrows
UPDATE ai_turns
SET status = 'queued',
    claimed_by = NULL,
    lease_expires_at = NULL,
    heartbeat_at = NULL,
    queued_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(turn_id) AND status = 'waiting_for_user';
