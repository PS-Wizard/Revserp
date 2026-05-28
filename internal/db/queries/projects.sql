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
