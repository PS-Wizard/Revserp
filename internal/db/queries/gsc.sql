-- name: CreateGoogleOAuthState :one
INSERT INTO google_oauth_states (
    state_token_hash,
    organization_id,
    user_id,
    project_id,
    return_path,
    expires_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6
)
RETURNING id, state_token_hash, organization_id, user_id, project_id, return_path, expires_at, created_at;

-- name: GetGoogleOAuthStateByTokenHash :one
SELECT id, state_token_hash, organization_id, user_id, project_id, return_path, expires_at, created_at
FROM google_oauth_states
WHERE state_token_hash = $1
LIMIT 1;

-- name: DeleteGoogleOAuthStateByID :execrows
DELETE FROM google_oauth_states
WHERE id = $1;

-- name: UpsertGoogleConnectionForOrganization :one
INSERT INTO google_connections (
    organization_id,
    connected_by_user_id,
    google_account_email,
    google_account_subject,
    encrypted_refresh_token,
    encrypted_access_token,
    access_token_expires_at,
    scope,
    status,
    last_error
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    'active',
    NULL
)
ON CONFLICT (organization_id) DO UPDATE SET
    connected_by_user_id = excluded.connected_by_user_id,
    google_account_email = COALESCE(excluded.google_account_email, google_connections.google_account_email),
    google_account_subject = COALESCE(excluded.google_account_subject, google_connections.google_account_subject),
    encrypted_refresh_token = COALESCE(excluded.encrypted_refresh_token, google_connections.encrypted_refresh_token),
    encrypted_access_token = excluded.encrypted_access_token,
    access_token_expires_at = excluded.access_token_expires_at,
    scope = excluded.scope,
    status = 'active',
    last_error = NULL,
    updated_at = now()
RETURNING id, organization_id, connected_by_user_id, google_account_email, google_account_subject, encrypted_refresh_token, encrypted_access_token, access_token_expires_at, scope, status, last_error, created_at, updated_at;

-- name: GetGoogleConnectionByOrganizationID :one
SELECT id, organization_id, connected_by_user_id, google_account_email, google_account_subject, encrypted_refresh_token, encrypted_access_token, access_token_expires_at, scope, status, last_error, created_at, updated_at
FROM google_connections
WHERE organization_id = $1
LIMIT 1;

-- name: UpdateGoogleConnectionTokens :one
UPDATE google_connections
SET encrypted_access_token = $2,
    encrypted_refresh_token = COALESCE($3, encrypted_refresh_token),
    access_token_expires_at = $4,
    scope = COALESCE($5, scope),
    status = $6,
    last_error = $7,
    updated_at = now()
WHERE id = $1
RETURNING id, organization_id, connected_by_user_id, google_account_email, google_account_subject, encrypted_refresh_token, encrypted_access_token, access_token_expires_at, scope, status, last_error, created_at, updated_at;

-- name: UpdateGoogleConnectionStatus :exec
UPDATE google_connections
SET status = $2,
    last_error = $3,
    updated_at = now()
WHERE id = $1;

-- name: GetProjectGSCConnectionByProjectID :one
SELECT id, project_id, google_connection_id, site_url, permission_level, created_at, updated_at
FROM project_gsc_connections
WHERE project_id = $1
LIMIT 1;

-- name: UpsertProjectGSCConnection :one
INSERT INTO project_gsc_connections (
    project_id,
    google_connection_id,
    site_url,
    permission_level
) VALUES (
    $1,
    $2,
    $3,
    $4
)
ON CONFLICT (project_id) DO UPDATE SET
    google_connection_id = excluded.google_connection_id,
    site_url = excluded.site_url,
    permission_level = excluded.permission_level,
    updated_at = now()
RETURNING id, project_id, google_connection_id, site_url, permission_level, created_at, updated_at;

-- name: DeleteProjectGSCConnectionByProjectID :execrows
DELETE FROM project_gsc_connections
WHERE project_id = $1;
