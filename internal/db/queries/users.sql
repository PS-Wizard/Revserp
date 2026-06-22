-- name: GetUserByAuthSubject :one
SELECT id, auth_provider, auth_subject, email, name, is_platform_admin, status, suspended_at, suspension_reason, created_at
FROM users
WHERE auth_provider = $1 AND auth_subject = $2
LIMIT 1;

-- name: GetUserByID :one
SELECT id, auth_provider, auth_subject, email, name, is_platform_admin, status, suspended_at, suspension_reason, created_at
FROM users
WHERE id = $1
LIMIT 1;

-- name: CreateUser :one
INSERT INTO users (
    auth_provider,
    auth_subject,
    email,
    name
) VALUES (
    $1,
    $2,
    $3,
    $4
)
RETURNING id, auth_provider, auth_subject, email, name, is_platform_admin, status, suspended_at, suspension_reason, created_at;

-- name: ListAllUsers :many
SELECT id, email, name, is_platform_admin, status, created_at
FROM users
WHERE status != 'deleted'
ORDER BY created_at DESC;

-- name: UpdateUserPlatformAdmin :exec
UPDATE users SET is_platform_admin = $2 WHERE id = $1;

-- name: UpdateUserStatus :exec
UPDATE users SET
    status = $2,
    suspended_at = CASE WHEN $2 = 'suspended' THEN NOW() ELSE NULL END,
    suspension_reason = CASE WHEN $2 = 'suspended' THEN $3 ELSE NULL END
WHERE id = $1;
