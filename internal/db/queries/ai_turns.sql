-- name: LockAIConversationForTurn :one
SELECT
    ac.project_id,
    p.organization_id,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.ai_monthly_message_limit, 50)::integer AS ai_monthly_message_limit,
    COALESCE(
        f.ai_allowed_reasoning_efforts,
        ARRAY['none', 'low', 'high', 'max']::TEXT[]
    ) AS ai_allowed_reasoning_efforts
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE ac.id = sqlc.arg(conversation_id)
FOR UPDATE OF ac
FOR KEY SHARE OF om;

-- name: FindAITurnByClientRequestID :one
SELECT
    t.id AS turn_id,
    user_message.id AS user_message_id,
    assistant_message.id AS assistant_message_id,
    t.request_hash
FROM ai_turns AS t
INNER JOIN ai_messages AS user_message ON user_message.turn_id = t.id
    AND user_message.role = 'user'
INNER JOIN ai_messages AS assistant_message ON assistant_message.turn_id = t.id
    AND assistant_message.role = 'assistant'
WHERE t.conversation_id = sqlc.arg(conversation_id)
  AND t.created_by_user_id = sqlc.arg(user_id)
  AND t.client_request_id = sqlc.arg(client_request_id);

-- name: GetCompletedCrawlForProject :one
SELECT id
FROM crawls
WHERE id = sqlc.arg(crawl_id)
  AND project_id = sqlc.arg(project_id)
  AND status = 'completed';

-- name: GetLatestCompletedCrawlForProject :one
SELECT id
FROM crawls
WHERE project_id = sqlc.arg(project_id)
  AND status = 'completed'
ORDER BY completed_at DESC NULLS LAST, id DESC
LIMIT 1;

-- name: HasActiveAITurnForConversation :one
SELECT EXISTS(
    SELECT 1
    FROM ai_turns
    WHERE conversation_id = sqlc.arg(conversation_id)
      AND status IN ('queued', 'running')
);

-- name: ReserveAIWorkspaceMonthlyMessage :one
INSERT INTO ai_workspace_monthly_usage (organization_id, period_start, used_messages)
SELECT
    sqlc.arg(organization_id),
    date_trunc('month', now() AT TIME ZONE 'UTC')::date,
    1
WHERE sqlc.arg(monthly_limit)::integer > 0
ON CONFLICT (organization_id, period_start) DO UPDATE
SET used_messages = ai_workspace_monthly_usage.used_messages + 1,
    updated_at = now()
WHERE ai_workspace_monthly_usage.used_messages < sqlc.arg(monthly_limit)::integer
RETURNING used_messages;

-- name: CreateAITurn :one
INSERT INTO ai_turns (
    conversation_id,
    created_by_user_id,
    status,
    requested_effort,
    effective_effort,
    model,
    prompt_version,
    crawl_id,
    client_request_id,
    request_hash
) VALUES (
    sqlc.arg(conversation_id),
    sqlc.arg(user_id),
    'queued',
    sqlc.arg(requested_effort),
    sqlc.arg(effective_effort),
    sqlc.arg(model),
    'chat-v1',
    sqlc.narg(crawl_id),
    sqlc.arg(client_request_id),
    sqlc.arg(request_hash)
)
RETURNING id;

-- name: CreateAIMessage :one
INSERT INTO ai_messages (turn_id, role, status, content)
VALUES (
    sqlc.arg(turn_id),
    sqlc.arg(role),
    sqlc.arg(status),
    sqlc.arg(content)
)
RETURNING id;

-- name: TouchAIConversation :exec
UPDATE ai_conversations
SET updated_at = now()
WHERE id = sqlc.arg(conversation_id);
