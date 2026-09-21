-- name: GetProjectGoogleAnalyticsConnectionByProjectID :one
SELECT id, project_id, google_connection_id, property_id, property_display_name, account_display_name, created_at, updated_at
FROM project_google_analytics_connections
WHERE project_id = $1
LIMIT 1;

-- name: UpsertProjectGoogleAnalyticsConnection :one
INSERT INTO project_google_analytics_connections (
    project_id,
    google_connection_id,
    property_id,
    property_display_name,
    account_display_name
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (project_id) DO UPDATE SET
    google_connection_id = excluded.google_connection_id,
    property_id = excluded.property_id,
    property_display_name = excluded.property_display_name,
    account_display_name = excluded.account_display_name,
    updated_at = now()
RETURNING id, project_id, google_connection_id, property_id, property_display_name, account_display_name, created_at, updated_at;

-- name: DeleteProjectGoogleAnalyticsConnectionByProjectID :execrows
DELETE FROM project_google_analytics_connections
WHERE project_id = $1;
