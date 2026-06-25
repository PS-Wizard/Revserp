-- name: GetProjectAutoCrawlSettings :one
SELECT
    project_id,
    enabled,
    config_snapshot,
    last_enqueued_at,
    created_at,
    updated_at
FROM project_auto_crawl_settings
WHERE project_id = $1
LIMIT 1;

-- name: UpsertProjectAutoCrawlSettings :one
INSERT INTO project_auto_crawl_settings (
    project_id,
    enabled,
    config_snapshot
) VALUES (
    $1,
    $2,
    $3
)
ON CONFLICT (project_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    config_snapshot = COALESCE(EXCLUDED.config_snapshot, project_auto_crawl_settings.config_snapshot),
    updated_at = now()
RETURNING project_id, enabled, config_snapshot, last_enqueued_at, created_at, updated_at;

-- name: ListDueAutoCrawlSettings :many
SELECT
    acs.project_id,
    acs.enabled,
    acs.config_snapshot,
    acs.last_enqueued_at,
    acs.created_at,
    acs.updated_at
FROM project_auto_crawl_settings AS acs
WHERE acs.enabled = true
  AND NOT EXISTS (
      SELECT 1
      FROM crawls AS c
      WHERE c.project_id = acs.project_id
        AND c.status IN ('queued', 'running')
  )
  AND (
      NOT EXISTS (
          SELECT 1
          FROM crawls AS c
          WHERE c.project_id = acs.project_id
            AND c.status = 'completed'
      )
      OR (
          SELECT MAX(c.completed_at)
          FROM crawls AS c
          WHERE c.project_id = acs.project_id
            AND c.status = 'completed'
      ) < $1
  )
ORDER BY acs.last_enqueued_at ASC NULLS FIRST
LIMIT $2;

-- name: UpdateAutoCrawlLastEnqueuedAt :exec
UPDATE project_auto_crawl_settings
SET last_enqueued_at = now(),
    updated_at = now()
WHERE project_id = $1;
