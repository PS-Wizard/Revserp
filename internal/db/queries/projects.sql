-- name: CreateProject :one
INSERT INTO projects (
    organization_id,
    name,
    base_url
) VALUES (
    $1,
    $2,
    $3
)
RETURNING id, organization_id, name, base_url, created_at;

-- name: GetProjectByIDForUser :one
SELECT
    p.id,
    p.organization_id,
    p.name,
    p.base_url,
    p.created_at
FROM projects AS p
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE p.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: ListProjectsForOrganization :many
SELECT
    p.id,
    p.organization_id,
    p.name,
    p.base_url,
    p.created_at
FROM projects AS p
WHERE p.organization_id = $1
ORDER BY p.created_at ASC;

-- name: DeleteProjectByIDForUser :execrows
DELETE FROM projects AS p
USING organization_members AS om
WHERE p.id = $1
  AND om.org_id = p.organization_id
  AND om.user_id = $2;

-- name: HasActiveCrawlForProject :one
SELECT EXISTS (
    SELECT 1
    FROM crawls AS c
    INNER JOIN projects AS p ON p.id = c.project_id
    INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    WHERE c.project_id = $1
      AND om.user_id = $2
      AND c.status IN ('queued', 'running')
) AS has_active_crawl;

-- name: ListProjectsForOrganizationForUser :many
SELECT
    p.id,
    p.organization_id,
    p.name,
    p.base_url,
    p.created_at
FROM projects AS p
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE p.organization_id = $1
  AND om.user_id = $2
ORDER BY p.created_at ASC;
