-- name: ListProjectMCPConnectionsByProjectID :many
SELECT id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at
FROM project_mcp_connections
WHERE project_id = $1
ORDER BY name ASC, id ASC;

-- name: GetProjectMCPConnectionByID :one
SELECT id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at
FROM project_mcp_connections
WHERE id = $1 AND project_id = $2
LIMIT 1;

-- name: CreateProjectMCPConnection :one
INSERT INTO project_mcp_connections (
    project_id,
    name,
    service,
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
    $6,
    now()
)
RETURNING id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at;

-- name: RenameProjectMCPConnection :one
UPDATE project_mcp_connections
SET name = $3,
    updated_at = now()
WHERE id = $1
  AND project_id = $2
RETURNING id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at;

-- name: ReplaceProjectMCPConnection :one
UPDATE project_mcp_connections
SET name = $3,
    endpoint_url = $4,
    encrypted_token = $5,
    revision = gen_random_uuid(),
    tools = $6,
    last_checked_at = now(),
    updated_at = now()
WHERE id = $1
  AND project_id = $2
  AND revision = $7
RETURNING id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at;

-- Lock ordering with the decision, permission, and replace paths is always
-- the connection row first, then approval rows, so concurrent writers
-- serialize instead of deadlocking.
-- name: LockProjectMCPConnectionByID :one
SELECT id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at
FROM project_mcp_connections
WHERE id = $1 AND project_id = $2
LIMIT 1
FOR UPDATE;

-- name: UpdateProjectMCPConnectionChecked :one
UPDATE project_mcp_connections
SET tools = $3,
    last_checked_at = now(),
    updated_at = now()
WHERE id = $1
  AND project_id = $2
  AND revision = $4
RETURNING id, project_id, name, service, endpoint_url, encrypted_token, revision, tools, last_checked_at, created_at, updated_at;

-- name: DeleteProjectMCPConnection :execrows
DELETE FROM project_mcp_connections
WHERE id = $1 AND project_id = $2;

-- name: ListMCPToolPermissionsForConnection :many
SELECT connection_id, tool_name, permission, updated_at
FROM project_mcp_tool_permissions
WHERE connection_id = $1;

-- name: UpsertMCPToolPermission :exec
INSERT INTO project_mcp_tool_permissions (connection_id, tool_name, permission, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (connection_id, tool_name) DO UPDATE SET
    permission = excluded.permission,
    updated_at = now();

-- name: DeleteMCPToolPermission :exec
DELETE FROM project_mcp_tool_permissions
WHERE connection_id = $1 AND tool_name = $2;

-- name: GetMCPConnectionForCall :one
SELECT c.revision::text AS revision, c.service AS service, COALESCE(p.permission, 'ask') AS permission, c.tools
FROM project_mcp_connections AS c
JOIN projects AS pr ON pr.id = c.project_id
JOIN organization_members AS om ON om.org_id = pr.organization_id AND om.user_id = sqlc.arg(user_id)
LEFT JOIN organization_features AS f ON f.org_id = pr.organization_id
LEFT JOIN project_mcp_tool_permissions AS p ON p.connection_id = c.id AND p.tool_name = sqlc.arg(tool_name)
WHERE c.id = sqlc.arg(connection_id)
  AND c.project_id = sqlc.arg(project_id)
  AND COALESCE(f.integrations, TRUE);

-- name: ListMCPConnectionsForTurn :many
SELECT c.id, c.project_id, c.name, c.service, c.endpoint_url, c.encrypted_token, c.revision::text AS revision, c.tools
FROM project_mcp_connections AS c
JOIN projects AS pr ON pr.id = c.project_id
JOIN organization_members AS om ON om.org_id = pr.organization_id AND om.user_id = sqlc.arg(user_id)
LEFT JOIN organization_features AS f ON f.org_id = pr.organization_id
WHERE c.project_id = sqlc.arg(project_id)
  AND COALESCE(f.integrations, TRUE)
ORDER BY c.name ASC, c.id ASC;
