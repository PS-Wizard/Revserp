-- name: CreateAIConversation :one
INSERT INTO ai_conversations (
    org_id,
    created_by_user_id,
    title
) VALUES (
    $1,
    $2,
    $3
)
RETURNING id, org_id, created_by_user_id, title, created_at, updated_at;

-- name: GetAIConversationForUser :one
SELECT
    ac.id,
    ac.org_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at
FROM ai_conversations AS ac
INNER JOIN organization_members AS om ON om.org_id = ac.org_id
WHERE ac.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: GetAIConversationForUserForUpdate :one
SELECT
    ac.id,
    ac.org_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at
FROM ai_conversations AS ac
INNER JOIN organization_members AS om ON om.org_id = ac.org_id
WHERE ac.id = $1
  AND om.user_id = $2
LIMIT 1
FOR UPDATE OF ac;

-- name: ListAIConversationsForOrgForUser :many
SELECT
    ac.id,
    ac.org_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at,
    (SELECT COUNT(*) FROM ai_messages am WHERE am.conversation_id = ac.id)::int AS message_count
FROM ai_conversations AS ac
INNER JOIN organization_members AS om ON om.org_id = ac.org_id
WHERE ac.org_id = $1
  AND om.user_id = $2
ORDER BY ac.updated_at DESC
LIMIT $3
OFFSET $4;

-- name: CreateAIMessage :one
INSERT INTO ai_messages (
    conversation_id,
    role,
    content,
    crawl_id,
    scope,
    model,
    reasoning_content,
    tool_calls,
    tool_call_id,
    tool_name
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9,
    $10
)
RETURNING id, conversation_id, role, content, crawl_id, scope, model, reasoning_content, tool_calls, tool_call_id, tool_name, created_at;

-- name: ListAIMessagesForConversationForUser :many
SELECT
    am.id,
    am.conversation_id,
    am.role,
    am.content,
    am.crawl_id,
    am.scope,
    am.model,
    am.reasoning_content,
    am.tool_calls,
    am.tool_call_id,
    am.tool_name,
    am.created_at
FROM ai_messages AS am
INNER JOIN ai_conversations AS ac ON ac.id = am.conversation_id
INNER JOIN organization_members AS om ON om.org_id = ac.org_id
WHERE am.conversation_id = $1
  AND om.user_id = $2
ORDER BY am.created_at ASC;

-- name: UpdateAIConversationTouched :one
UPDATE ai_conversations
SET updated_at = now()
WHERE id = $1
RETURNING id, org_id, created_by_user_id, title, created_at, updated_at;

-- name: DeleteAIConversationForUser :one
DELETE FROM ai_conversations AS ac
USING organization_members AS om
WHERE ac.id = $1
  AND om.org_id = ac.org_id
  AND om.user_id = $2
RETURNING ac.id;
