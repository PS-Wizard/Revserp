-- name: CreateAIAudit :one
INSERT INTO ai_audits (
    project_id,
    crawl_id,
    status,
    score,
    error_message,
    started_at,
    completed_at,
    location_id
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8
)
RETURNING id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at, location_id;

-- name: GetAIAuditByIDForUser :one
SELECT
    aa.id,
    aa.project_id,
    aa.crawl_id,
    aa.status,
    aa.score,
    aa.error_message,
    aa.started_at,
    aa.completed_at,
    aa.created_at,
    aa.updated_at,
    aa.location_id
FROM ai_audits AS aa
INNER JOIN projects AS p ON p.id = aa.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE aa.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: GetAIAuditForWorker :one
SELECT id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at, location_id
FROM ai_audits
WHERE id = sqlc.arg(id)::uuid
  AND project_id = sqlc.arg(project_id)::uuid
LIMIT 1;

-- name: CountAIAuditsForProject :one
SELECT COUNT(*)
FROM ai_audits
WHERE project_id = $1
  AND location_id IS NULL
  AND ($2 = '' OR status = $2);

-- name: ListAIAuditsForProject :many
SELECT
    id,
    project_id,
    crawl_id,
    status,
    score,
    error_message,
    started_at,
    completed_at,
    created_at,
    updated_at,
    location_id
FROM ai_audits
WHERE project_id = $1
  AND location_id IS NULL
  AND ($2 = '' OR status = $2)
ORDER BY created_at DESC
LIMIT $3
OFFSET $4;

-- name: CountAIAuditsForLocation :one
SELECT COUNT(*)
FROM ai_audits
WHERE project_id = sqlc.arg(project_id)::uuid
  AND location_id = sqlc.arg(location_id)::uuid
  AND (sqlc.arg(status_filter)::text = '' OR status = sqlc.arg(status_filter)::text);

-- name: ListAIAuditsForLocation :many
SELECT
    id,
    project_id,
    crawl_id,
    status,
    score,
    error_message,
    started_at,
    completed_at,
    created_at,
    updated_at,
    location_id
FROM ai_audits
WHERE project_id = sqlc.arg(project_id)::uuid
  AND location_id = sqlc.arg(location_id)::uuid
  AND (sqlc.arg(status_filter)::text = '' OR status = sqlc.arg(status_filter)::text)
ORDER BY created_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: GetAIAuditByCrawlAndProject :one
SELECT id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at, location_id
FROM ai_audits
WHERE project_id = $1 AND crawl_id = $2 AND location_id IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: GetAIAuditByLocationCrawlAndProject :one
SELECT id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at, location_id
FROM ai_audits
WHERE project_id = sqlc.arg(project_id)::uuid
  AND location_id = sqlc.arg(location_id)::uuid
  AND crawl_id = sqlc.arg(crawl_id)::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: GetActiveAIAuditByCrawlAndProject :one
SELECT id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at, location_id
FROM ai_audits
WHERE project_id = $1 AND crawl_id = $2 AND location_id IS NULL AND status IN ('queued', 'running')
LIMIT 1;

-- name: GetActiveAIAuditByLocationAndProject :one
SELECT id, project_id, crawl_id, status, score, error_message, started_at, completed_at, created_at, updated_at, location_id
FROM ai_audits
WHERE project_id = sqlc.arg(project_id)::uuid
  AND location_id = sqlc.arg(location_id)::uuid
  AND status IN ('queued', 'running')
LIMIT 1;

-- name: UpdateAIAuditStatus :exec
UPDATE ai_audits
SET status = $2, error_message = $3, started_at = $4, completed_at = $5, updated_at = NOW()
WHERE id = $1;

-- name: ReclaimStaleRunningAIAudits :exec
-- Marks ai_audits rows orphaned by a crashed worker process (stuck in
-- 'running' past the cutoff) as failed, mirroring ReclaimStaleRunningAIWorkerJobs.
UPDATE ai_audits
SET status = 'failed', error_message = 'reclaimed: stale running audit', completed_at = NOW(), updated_at = NOW()
WHERE status = 'running'
  AND started_at < $1;

-- name: ReserveAIWorkspaceMonthlyAudit :one
-- Atomically reserves one visibility audit for the month. Returns no rows
-- when the workspace limit is 0 (audits disabled) or already exhausted,
-- mirroring ReserveAIWorkspaceMonthlyMessage.
INSERT INTO ai_workspace_monthly_usage (organization_id, period_start, used_audits)
SELECT
    sqlc.arg(organization_id),
    date_trunc('month', now() AT TIME ZONE 'UTC')::date,
    1
WHERE sqlc.arg(monthly_limit)::integer > 0
ON CONFLICT (organization_id, period_start) DO UPDATE
SET used_audits = ai_workspace_monthly_usage.used_audits + 1,
    updated_at = now()
WHERE ai_workspace_monthly_usage.used_audits < sqlc.arg(monthly_limit)::integer
RETURNING used_audits;

-- name: FailActiveAIAuditsForCrawl :execrows
-- Setup retry path: a queued/running audit orphaned by a crashed or failed
-- visibility job would otherwise block creating the fresh retry audit on the
-- one-active-per-crawl index. Terminal audits are never touched, so history
-- stays append-only and a failed run is never reused.
UPDATE ai_audits
SET status = 'failed',
    error_message = 'superseded by project setup retry',
    completed_at = now(),
    updated_at = now()
WHERE project_id = sqlc.arg(project_id)
  AND crawl_id = sqlc.arg(crawl_id)
  AND location_id IS NULL
  AND status IN ('queued', 'running');
