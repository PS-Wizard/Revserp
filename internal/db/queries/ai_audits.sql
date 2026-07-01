-- name: CreateAIAudit :one
INSERT INTO ai_audits (
    project_id,
    crawl_id,
    status,
    score,
    error_message,
    started_at,
    completed_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7
)
RETURNING id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at;

-- name: GetAIAuditByIDForUser :one
SELECT
    aa.id,
    aa.project_id,
    aa.crawl_id,
    aa.status,
    aa.score,
    aa.error_message,
    aa.started_at,
    aa.completed_at,
    aa.created_at,
    aa.updated_at
FROM ai_audits AS aa
INNER JOIN projects AS p ON p.id = aa.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE aa.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: CountAIAuditsForProject :one
SELECT COUNT(*)
FROM ai_audits
WHERE project_id = $1
  AND ($2 = '' OR status = $2);

-- name: ListAIAuditsForProject :many
SELECT
    id,
    project_id,
    crawl_id,
    status,
    score,
    error_message,
    started_at,
    completed_at,
    created_at,
    updated_at
FROM ai_audits
WHERE project_id = $1
  AND ($2 = '' OR status = $2)
ORDER BY created_at DESC
LIMIT $3
OFFSET $4;

-- name: GetAIAuditByCrawlAndProject :one
SELECT id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at
FROM ai_audits
WHERE project_id = $1 AND crawl_id = $2
LIMIT 1;

-- name: DeleteAIAuditByID :exec
DELETE FROM ai_audits WHERE id = $1;

-- name: UpdateAIAuditStatus :exec
UPDATE ai_audits
SET status = $2, error_message = $3, started_at = $4, completed_at = $5, updated_at = NOW()
WHERE id = $1;
