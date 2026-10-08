-- name: GetLocationGSCBinding :one
SELECT location_id, mode, google_connection_id, site_url, permission_level
FROM location_gsc_connections
WHERE location_id = $1
LIMIT 1;

-- name: UpsertLocationGSCBinding :exec
INSERT INTO location_gsc_connections (
    location_id,
    mode,
    google_connection_id,
    site_url,
    permission_level,
    updated_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    now()
)
ON CONFLICT (location_id) DO UPDATE SET
    mode = excluded.mode,
    google_connection_id = excluded.google_connection_id,
    site_url = excluded.site_url,
    permission_level = excluded.permission_level,
    updated_at = now();

-- name: GetLocationGoogleAnalyticsBinding :one
SELECT location_id, mode, google_connection_id, property_id, property_display_name, account_display_name
FROM location_google_analytics_connections
WHERE location_id = $1
LIMIT 1;

-- name: UpsertLocationGoogleAnalyticsBinding :exec
INSERT INTO location_google_analytics_connections (
    location_id,
    mode,
    google_connection_id,
    property_id,
    property_display_name,
    account_display_name,
    updated_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    now()
)
ON CONFLICT (location_id) DO UPDATE SET
    mode = excluded.mode,
    google_connection_id = excluded.google_connection_id,
    property_id = excluded.property_id,
    property_display_name = excluded.property_display_name,
    account_display_name = excluded.account_display_name,
    updated_at = now();
