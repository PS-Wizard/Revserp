-- name: GetProjectSetupByProjectID :one
SELECT
    id,
    organization_id,
    project_id,
    requested_by_user_id,
    crawl_id,
    status,
    error,
    failed_step,
    visibility_skip_reason,
    created_at,
    updated_at,
    completed_at
FROM project_setup
WHERE project_id = $1
LIMIT 1;

-- name: GetProjectSetupByProjectIDForUpdate :one
-- Row-locks the setup for the duration of a worker transaction so two workers
-- cannot both advance the same setup step. The guard lives in
-- UpdateProjectSetupStatus(expected_status); this only serializes the decision.
SELECT
    id,
    organization_id,
    project_id,
    requested_by_user_id,
    crawl_id,
    status,
    error,
    failed_step,
    visibility_skip_reason,
    created_at,
    updated_at,
    completed_at
FROM project_setup
WHERE project_id = $1
LIMIT 1
FOR UPDATE;

-- name: CreateProjectSetup :one
-- Insert-only: the unique project_id index makes a re-click a no-op, so calling
-- this twice never creates a second setup row.
INSERT INTO project_setup (
    organization_id,
    project_id,
    requested_by_user_id,
    status
) VALUES (
    $1,
    $2,
    $3,
    $4
)
ON CONFLICT (project_id) DO NOTHING
RETURNING id, organization_id, project_id, requested_by_user_id, crawl_id, status, error, failed_step, visibility_skip_reason, created_at, updated_at, completed_at;

-- name: StartProjectSetup :one
-- Ready-to-crawling start used by the owner's setup POST. It stamps the acting
-- owner as requested_by_user_id and links the first crawl in one guarded
-- UPDATE. The status = 'ready' guard makes a concurrent or repeated start
-- update zero rows (pgx.ErrNoRows), so the crawl it was handed rolls back
-- with the transaction and never leaks.
UPDATE project_setup
SET requested_by_user_id = sqlc.arg(requested_by_user_id),
    status = 'crawling',
    crawl_id = sqlc.arg(crawl_id),
    error = NULL,
    failed_step = NULL,
    visibility_skip_reason = NULL,
    completed_at = NULL,
    updated_at = now()
WHERE project_id = sqlc.arg(project_id)
  AND status = 'ready'
RETURNING id, organization_id, project_id, requested_by_user_id, crawl_id, status, error, failed_step, visibility_skip_reason, created_at, updated_at, completed_at;

-- name: UpdateProjectSetupStatus :one
-- Worker entry point for non-crawl transitions. The expected_status guard makes
-- a stale or out-of-order event update zero rows instead of moving the workflow
-- backward. Non-failed transitions clear the prior error, failed_step and
-- completed_at; a failed transition keeps the supplied failed_step and error and
-- stamps completed_at.
UPDATE project_setup
SET status = sqlc.arg(status),
    -- A retry by a different current owner re-stamps the requester; worker
    -- transitions leave it NULL and keep the existing owner. Later profile and
    -- feature checks then never run as a stale or deleted user.
    requested_by_user_id = COALESCE(sqlc.narg(requested_by_user_id), requested_by_user_id),
    error = CASE WHEN sqlc.arg(status) = 'failed' THEN sqlc.narg(error) ELSE NULL END,
    failed_step = CASE WHEN sqlc.arg(status) = 'failed' THEN sqlc.narg(failed_step) ELSE NULL END,
    visibility_skip_reason = sqlc.narg(visibility_skip_reason),
    crawl_id = COALESCE(sqlc.narg(crawl_id), crawl_id),
    updated_at = now(),
    completed_at = CASE WHEN sqlc.arg(status) IN ('completed', 'failed') THEN now() ELSE NULL END
WHERE project_id = sqlc.arg(project_id)
  AND status = sqlc.arg(expected_status)
RETURNING id, organization_id, project_id, requested_by_user_id, crawl_id, status, error, failed_step, visibility_skip_reason, created_at, updated_at, completed_at;

-- name: AdvanceProjectSetupFromCrawling :one
-- Crawl-to-profile transition used by crawl finalization. The WHERE clause is
-- the idempotency guard: only a setup still in 'crawling' linked to this exact
-- crawl can move, so duplicate terminal handling and unrelated crawls update
-- zero rows (pgx.ErrNoRows). Callers enqueue the profile bootstrap job only when
-- a row is returned, inside the same transaction as the crawl status write.
UPDATE project_setup
SET status = sqlc.arg(status),
    error = sqlc.narg(error),
    failed_step = sqlc.narg(failed_step),
    updated_at = now(),
    completed_at = CASE WHEN sqlc.arg(status) IN ('completed', 'failed') THEN now() ELSE NULL END
WHERE crawl_id = sqlc.arg(crawl_id)
  AND status = 'crawling'
RETURNING id, organization_id, project_id, requested_by_user_id, crawl_id, status, error, failed_step, visibility_skip_reason, created_at, updated_at, completed_at;
