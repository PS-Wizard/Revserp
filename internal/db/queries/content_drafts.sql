-- Content drafts. User-scoped queries authorize through the project's
-- organization membership, exactly like the ai_conversations user-scoped
-- queries. Project-scoped queries skip the membership check; AI tools call
-- them with a project id already resolved from server-side scope.

-- name: CreateContentDraftForUser :one
INSERT INTO content_drafts (org_id, project_id, created_by_user_id, title)
SELECT p.organization_id, p.id, sqlc.arg(user_id), COALESCE(sqlc.narg(title), '') AS title
FROM projects AS p
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id)
  AND om.user_id = sqlc.arg(user_id)
RETURNING *;

-- name: GetContentDraftForUser :one
SELECT
    d.id,
    d.org_id,
    d.project_id,
    d.created_by_user_id,
    d.title,
    d.meta_title,
    d.meta_description,
    d.target_keyword,
    d.body,
    d.created_at,
    d.updated_at,
    ac.id AS conversation_id
FROM content_drafts AS d
INNER JOIN projects AS p ON p.id = d.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
LEFT JOIN ai_conversations AS ac ON ac.content_draft_id = d.id
WHERE d.id = sqlc.arg(draft_id)
  AND om.user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: ListContentDraftsForProjectForUser :many
SELECT
    d.id,
    d.title,
    d.target_keyword,
    ac.id AS conversation_id,
    d.updated_at
FROM content_drafts AS d
INNER JOIN projects AS p ON p.id = d.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
LEFT JOIN ai_conversations AS ac ON ac.content_draft_id = d.id
WHERE d.project_id = sqlc.arg(project_id)
  AND om.user_id = sqlc.arg(user_id)
ORDER BY d.updated_at DESC, d.id DESC;

-- name: UpdateContentDraftForUser :one
UPDATE content_drafts d
SET title = COALESCE(sqlc.narg(title), title),
    meta_title = COALESCE(sqlc.narg(meta_title), meta_title),
    meta_description = COALESCE(sqlc.narg(meta_description), meta_description),
    target_keyword = COALESCE(sqlc.narg(target_keyword), target_keyword),
    body = COALESCE(sqlc.narg(body), body),
    updated_at = now()
FROM projects p
INNER JOIN organization_members om ON om.org_id = p.organization_id
WHERE d.id = sqlc.arg(draft_id)
  AND d.project_id = p.id
  AND om.user_id = sqlc.arg(user_id)
RETURNING d.*;

-- name: DeleteContentDraftForUser :execrows
DELETE FROM content_drafts AS d
USING projects AS p, organization_members AS om
WHERE d.id = sqlc.arg(draft_id)
  AND d.project_id = p.id
  AND om.org_id = p.organization_id
  AND om.user_id = sqlc.arg(user_id);

-- name: GetContentDraftForProject :one
SELECT *
FROM content_drafts
WHERE id = sqlc.arg(id)
  AND project_id = sqlc.arg(project_id)
LIMIT 1;

-- name: UpdateContentDraftForProject :one
UPDATE content_drafts
SET title = COALESCE(sqlc.narg(title), title),
    meta_title = COALESCE(sqlc.narg(meta_title), meta_title),
    meta_description = COALESCE(sqlc.narg(meta_description), meta_description),
    target_keyword = COALESCE(sqlc.narg(target_keyword), target_keyword),
    body = COALESCE(sqlc.narg(body), body),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND project_id = sqlc.arg(project_id)
RETURNING *;
