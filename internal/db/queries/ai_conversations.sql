-- name: CreateAIConversationForUser :one
INSERT INTO ai_conversations (project_id, created_by_user_id, title)
SELECT p.id, sqlc.arg(user_id), 'New conversation'
FROM projects AS p
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id)
  AND om.user_id = sqlc.arg(user_id)
RETURNING id, project_id, created_by_user_id, title, created_at, updated_at;

-- name: CountAIConversationsForProjectForUser :one
SELECT COUNT(*)
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.project_id = sqlc.arg(project_id)
  AND om.user_id = sqlc.arg(user_id);

-- name: ListAIConversationsForProjectForUser :many
SELECT
    ac.id,
    ac.project_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.project_id = sqlc.arg(project_id)
  AND om.user_id = sqlc.arg(user_id)
ORDER BY ac.updated_at DESC, ac.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: GetAIConversationByIDForUser :one
SELECT
    ac.id,
    ac.project_id,
    ac.created_by_user_id,
    ac.title,
    ac.created_at,
    ac.updated_at
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ac.id = sqlc.arg(conversation_id)
  AND om.user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: DeleteAIConversationByIDForUser :execrows
DELETE FROM ai_conversations AS ac
USING projects AS p, organization_members AS om
WHERE ac.id = sqlc.arg(conversation_id)
  AND ac.project_id = p.id
  AND om.org_id = p.organization_id
  AND om.user_id = sqlc.arg(user_id);
