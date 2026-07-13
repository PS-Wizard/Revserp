-- name: GetProjectAutoCrawlSettings :one
SELECT
    project_id,
    enabled,
    config_snapshot,
    last_enqueued_at,
    created_at,
    updated_at,
    frequency_days,
    run_at,
    timezone,
    next_run_at
FROM project_auto_crawl_settings
WHERE project_id = $1
LIMIT 1;

-- name: UpsertProjectAutoCrawlSettings :one
INSERT INTO project_auto_crawl_settings (
    project_id,
    enabled,
    config_snapshot,
    frequency_days,
    run_at,
    timezone,
    next_run_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7
)
ON CONFLICT (project_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    config_snapshot = COALESCE(EXCLUDED.config_snapshot, project_auto_crawl_settings.config_snapshot),
    frequency_days = EXCLUDED.frequency_days,
    run_at = EXCLUDED.run_at,
    timezone = EXCLUDED.timezone,
    next_run_at = EXCLUDED.next_run_at,
    updated_at = now()
RETURNING project_id, enabled, config_snapshot, last_enqueued_at, created_at, updated_at, frequency_days, run_at, timezone, next_run_at;

-- name: ListDueAutoCrawlSettings :many
SELECT
    acs.project_id,
    acs.enabled,
    acs.config_snapshot,
    acs.last_enqueued_at,
    acs.created_at,
    acs.updated_at,
    acs.frequency_days,
    acs.run_at,
    acs.timezone,
    acs.next_run_at
FROM project_auto_crawl_settings AS acs
WHERE acs.enabled = true
  AND acs.next_run_at IS NOT NULL
  AND acs.next_run_at <= now()
  AND NOT EXISTS (
      SELECT 1
      FROM crawls AS c
      WHERE c.project_id = acs.project_id
        AND c.status IN ('queued', 'running')
  )
ORDER BY acs.next_run_at ASC
LIMIT $1;

-- name: UpdateAutoCrawlEnqueued :exec
UPDATE project_auto_crawl_settings
SET last_enqueued_at = now(),
    next_run_at = $2,
    updated_at = now()
WHERE project_id = $1;
