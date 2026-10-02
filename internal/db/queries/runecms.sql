-- name: GetProjectCMSConnectionByProjectID :one
SELECT project_id, provider, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at
FROM project_cms_connections
WHERE project_id = $1
LIMIT 1;

-- name: UpsertProjectCMSConnection :one
INSERT INTO project_cms_connections (
    project_id,
    provider,
    endpoint_url,
    encrypted_token,
    tools,
    last_checked_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    now()
)
ON CONFLICT (project_id) DO UPDATE SET
    provider = excluded.provider,
    endpoint_url = excluded.endpoint_url,
    encrypted_token = excluded.encrypted_token,
    revision = gen_random_uuid(),
    tools = excluded.tools,
    last_checked_at = excluded.last_checked_at,
    updated_at = now()
RETURNING project_id, provider, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at;

-- name: UpdateProjectCMSConnectionChecked :one
UPDATE project_cms_connections
SET tools = $2,
    last_checked_at = now(),
    updated_at = now()
WHERE project_id = $1
  AND revision = $3
RETURNING project_id, provider, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at;

-- name: DeleteProjectCMSConnectionByProjectID :execrows
DELETE FROM project_cms_connections
WHERE project_id = $1;
