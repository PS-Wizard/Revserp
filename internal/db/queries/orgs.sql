-- name: CreateOrganization :one
INSERT INTO organizations (name)
VALUES ($1)
RETURNING id, name, created_at;

-- name: AddOrganizationMember :one
INSERT INTO organization_members (
    org_id,
    user_id,
    role
) VALUES (
    $1,
    $2,
    $3
)
RETURNING org_id, user_id, role, created_at;

-- name: ListOrganizationsForUser :many
SELECT
    o.id,
    o.name,
    o.created_at,
    om.role
FROM organizations AS o
INNER JOIN organization_members AS om ON om.org_id = o.id
WHERE om.user_id = $1
ORDER BY o.created_at ASC;
