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
    s.id,
    s.user_id,
    s.session_token_hash,
    s.supabase_access_token,
    s.supabase_refresh_token,
    s.supabase_access_token_expires_at,
    s.active_org_id,
    s.created_at,
    s.updated_at,
    s.last_used_at,
    s.expires_at,
    s.revoked_at,
    s.previous_session_token_hash,
    s.previous_session_token_expires_at,
    s.supabase_refresh_retry_after,
    s.supabase_refresh_disabled_at,
    u.auth_provider,
    u.auth_subject,
    u.email,
    u.name,
    u.status AS user_status
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.session_token_hash = $1
   OR (
       s.previous_session_token_hash = $1
       AND s.previous_session_token_expires_at > now()
   )
LIMIT 1;

-- name: GetSessionByTokenHashForUpdate :one
SELECT
    s.id,
    s.user_id,
    s.session_token_hash,
    s.supabase_access_token,
    s.supabase_refresh_token,
    s.supabase_access_token_expires_at,
    s.active_org_id,
    s.created_at,
    s.updated_at,
    s.last_used_at,
    s.expires_at,
    s.revoked_at,
    s.previous_session_token_hash,
    s.previous_session_token_expires_at,
    s.supabase_refresh_retry_after,
    s.supabase_refresh_disabled_at,
    u.auth_provider,
    u.auth_subject,
    u.email,
    u.name,
    u.status AS user_status
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.session_token_hash = $1
   OR (
       s.previous_session_token_hash = $1
       AND s.previous_session_token_expires_at > now()
   )
LIMIT 1
FOR UPDATE OF s;

-- name: RotateSession :exec
UPDATE sessions
SET previous_session_token_hash = session_token_hash,
    previous_session_token_expires_at = $3,
    session_token_hash = $2,
    supabase_access_token = $4,
    supabase_refresh_token = $5,
    supabase_access_token_expires_at = $6,
    expires_at = $7,
    supabase_refresh_retry_after = NULL,
    supabase_refresh_disabled_at = NULL,
    updated_at = now(),
    last_used_at = now()
WHERE id = $1;

-- name: DelaySessionRenewal :exec
UPDATE sessions
SET supabase_refresh_retry_after = $2,
    updated_at = now()
WHERE id = $1;

-- name: SaveUnverifiedSessionRefresh :exec
UPDATE sessions
SET supabase_access_token = $2,
    supabase_refresh_token = $3,
    supabase_access_token_expires_at = $4,
    supabase_refresh_retry_after = $5,
    updated_at = now()
WHERE id = $1;

-- name: DisableSessionRenewal :exec
UPDATE sessions
SET supabase_refresh_disabled_at = now(),
    supabase_refresh_retry_after = NULL,
    updated_at = now()
WHERE id = $1;

-- name: UpdateSessionActiveOrganization :exec
UPDATE sessions
SET active_org_id = $2,
    updated_at = now(),
    last_used_at = now()
WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions
SET revoked_at = now(),
    updated_at = now(),
    last_used_at = now()
WHERE id = $1;
