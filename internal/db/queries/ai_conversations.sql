-- name: CreateAIConversation :one
INSERT INTO ai_conversations (
    project_id,
    crawl_id,
    created_by_user_id,
    title
) VALUES (
    $1,
    $2,
    $3,
    $4
)
RETURNING id, project_id, crawl_id, created_by_user_id, title, created_at, updated_at;

-- name: GetAIConversationForUser :one
SELECT
    ac.id,
    ac.project_id,
    ac.crawl_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: GetAIConversationForUserForUpdate :one
SELECT
    ac.id,
    ac.project_id,
    ac.crawl_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.id = $1
  AND om.user_id = $2
LIMIT 1
FOR UPDATE;

-- name: ListAIConversationsForProjectForUser :many
SELECT
    ac.id,
    ac.project_id,
    ac.crawl_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at,
    (SELECT COUNT(*) FROM ai_messages am WHERE am.conversation_id = ac.id)::int AS message_count
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.project_id = $1
  AND om.user_id = $2
ORDER BY ac.updated_at DESC
LIMIT $3
OFFSET $4;

-- name: ListAIConversationsForCrawlForUser :many
SELECT
    ac.id,
    ac.project_id,
    ac.crawl_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at,
    (SELECT COUNT(*) FROM ai_messages am WHERE am.conversation_id = ac.id)::int AS message_count
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.project_id = $1
  AND ac.crawl_id = $2
  AND om.user_id = $3
ORDER BY ac.updated_at DESC
LIMIT $4
OFFSET $5;

-- name: CreateAIMessage :one
INSERT INTO ai_messages (
    conversation_id,
    role,
    content,
    crawl_id,
    scope,
    model
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6
)
RETURNING id, conversation_id, role, content, crawl_id, scope, model, created_at;

-- name: ListAIMessagesForConversationForUser :many
SELECT
    am.id,
    am.conversation_id,
    am.role,
    am.content,
    am.crawl_id,
    am.scope,
    am.model,
    am.created_at
FROM ai_messages AS am
INNER JOIN ai_conversations AS ac ON ac.id = am.conversation_id
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE am.conversation_id = $1
  AND om.user_id = $2
ORDER BY am.created_at ASC;

-- name: UpdateAIConversationTouched :one
UPDATE ai_conversations
SET updated_at = now()
WHERE id = $1
RETURNING id, project_id, crawl_id, created_by_user_id, title, created_at, updated_at;

-- name: DeleteAIConversationForUser :one
DELETE FROM ai_conversations AS ac
USING projects AS p, organization_members AS om
WHERE ac.id = $1
  AND p.id = ac.project_id
  AND om.org_id = p.organization_id
  AND om.user_id = $2
RETURNING ac.id;
