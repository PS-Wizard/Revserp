-- name: CreateOrganizationInvite :one
INSERT INTO organization_invites (
    organization_id,
    created_by_user_id,
    token_hash,
    max_uses,
    expires_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5
)
RETURNING id, organization_id, created_by_user_id, token_hash, max_uses, used_count, expires_at, revoked_at, created_at;

-- name: ListOrganizationInvites :many
SELECT
    id,
    organization_id,
    created_by_user_id,
    token_hash,
    max_uses,
    used_count,
    expires_at,
    revoked_at,
    created_at
FROM organization_invites
WHERE organization_id = $1
ORDER BY created_at DESC;

-- name: GetOrganizationInviteByID :one
SELECT
    id,
    organization_id,
    created_by_user_id,
    token_hash,
    max_uses,
    used_count,
    expires_at,
    revoked_at,
    created_at
FROM organization_invites
WHERE id = $1
LIMIT 1;

-- name: RevokeOrganizationInvite :execrows
UPDATE organization_invites
SET revoked_at = now()
WHERE id = $1
  AND organization_id = $2
  AND revoked_at IS NULL;

-- name: GetOrganizationInviteByTokenHash :one
SELECT
    oi.id,
    oi.organization_id,
    oi.created_by_user_id,
    oi.token_hash,
    oi.max_uses,
    oi.used_count,
    oi.expires_at,
    oi.revoked_at,
    oi.created_at,
    o.name AS organization_name
FROM organization_invites AS oi
INNER JOIN organizations AS o ON o.id = oi.organization_id
WHERE oi.token_hash = $1
LIMIT 1;

-- name: GetOrganizationInviteByTokenHashForUpdate :one
SELECT
    id,
    organization_id,
    created_by_user_id,
    token_hash,
    max_uses,
    used_count,
    expires_at,
    revoked_at,
    created_at
FROM organization_invites
WHERE token_hash = $1
LIMIT 1
FOR UPDATE;

-- name: IncrementOrganizationInviteUsedCount :exec
UPDATE organization_invites
SET used_count = used_count + 1
WHERE id = $1;
