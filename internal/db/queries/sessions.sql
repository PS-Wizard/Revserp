-- name: CreateSession :one
INSERT INTO sessions (
    user_id,
    session_token_hash,
    supabase_access_token,
    supabase_refresh_token,
    supabase_access_token_expires_at,
    active_org_id,
    expires_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7
)
RETURNING id, user_id, session_token_hash, supabase_access_token, supabase_refresh_token, supabase_access_token_expires_at, active_org_id, created_at, updated_at, last_used_at, expires_at, revoked_at;

-- name: GetSessionByTokenHash :one
SELECT
    id,
    user_id,
    session_token_hash,
    supabase_access_token,
    supabase_refresh_token,
    supabase_access_token_expires_at,
    active_org_id,
    created_at,
    updated_at,
    last_used_at,
    expires_at,
    revoked_at
FROM sessions
WHERE session_token_hash = $1
LIMIT 1;

-- name: UpdateSessionTokens :exec
UPDATE sessions
SET supabase_access_token = $2,
    supabase_refresh_token = $3,
    supabase_access_token_expires_at = $4,
    updated_at = now(),
    last_used_at = now()
WHERE id = $1;

-- name: UpdateSessionActiveOrganization :exec
UPDATE sessions
SET active_org_id = $2,
    updated_at = now(),
    last_used_at = now()
WHERE id = $1 AND user_id = $3;

-- name: RevokeSession :exec
UPDATE sessions
SET revoked_at = now(),
    updated_at = now(),
    last_used_at = now()
WHERE id = $1;

-- name: RevokeAllSessionsForUser :exec
UPDATE sessions
SET revoked_at = now(),
    updated_at = now(),
    last_used_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;
