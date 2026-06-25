-- name: CreateCrawl :one
INSERT INTO crawls (
    project_id,
    requested_by_user_id,
    source,
    status,
    config_snapshot,
    started_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6
)
RETURNING id, project_id, status, config_snapshot, urls_discovered, urls_crawled, max_depth_reached, google_psi_results, has_llms_txt, seo_score, aeo_score, pagespeed_score, overall_score, started_at, completed_at, created_at;

-- name: GetCrawlByIDForUser :one
SELECT
    c.id,
    c.project_id,
    c.status,
    c.config_snapshot,
    c.urls_discovered,
    c.urls_crawled,
    c.max_depth_reached,
    c.google_psi_results,
    c.has_llms_txt,
    c.seo_score,
    c.aeo_score,
    c.pagespeed_score,
    c.overall_score,
    c.started_at,
    c.completed_at,
    c.created_at
FROM crawls AS c
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE c.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: DeleteCrawlByIDForUser :one
DELETE FROM crawls AS c
USING projects AS p, organization_members AS om
WHERE c.id = $1
  AND c.project_id = p.id
  AND p.organization_id = om.org_id
  AND om.user_id = $2
  AND c.status NOT IN ('queued', 'running')
RETURNING c.id;

-- name: CancelCrawlByIDForUser :one
UPDATE crawls AS c
SET status = 'cancelled',
    completed_at = now()
FROM projects AS p, organization_members AS om
WHERE c.id = $1
  AND c.project_id = p.id
  AND p.organization_id = om.org_id
  AND om.user_id = $2
  AND c.status IN ('queued', 'running')
RETURNING c.id;

-- name: CountCrawlsForProject :one
SELECT COUNT(*)
FROM crawls
WHERE project_id = $1
  AND ($2 = '' OR status = $2);

-- name: ListCrawlsForProject :many
SELECT
    id,
    project_id,
    status,
    config_snapshot,
    urls_discovered,
    urls_crawled,
    max_depth_reached,
    google_psi_results,
    has_llms_txt,
    seo_score,
    aeo_score,
    pagespeed_score,
    overall_score,
    started_at,
    completed_at,
    created_at
FROM crawls
WHERE project_id = $1
  AND ($2 = '' OR status = $2)
ORDER BY created_at DESC
LIMIT $3
OFFSET $4;

-- name: MarkCrawlRunning :exec
UPDATE crawls
SET status = 'running',
    started_at = now(),
    completed_at = NULL
WHERE id = $1;

-- name: MarkCrawlCompleted :exec
UPDATE crawls
SET status = 'completed',
    urls_discovered = $2,
    urls_crawled = $3,
    max_depth_reached = $4,
    completed_at = now()
WHERE id = $1;

-- name: MarkCrawlFailed :exec
UPDATE crawls
SET status = 'failed',
    urls_discovered = $2,
    urls_crawled = $3,
    max_depth_reached = $4,
    completed_at = now()
WHERE id = $1;

-- name: ClaimNextQueuedCrawlManual :one
WITH candidate AS (
    SELECT c.id
    FROM crawls AS c
    WHERE c.status = 'queued'
      AND c.source = 'manual'
      AND NOT EXISTS (
          SELECT 1
          FROM crawls AS running
          WHERE running.status = 'running'
            AND running.requested_by_user_id = c.requested_by_user_id
            AND c.requested_by_user_id IS NOT NULL
      )
    ORDER BY c.created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE crawls AS c
SET status = 'running',
    started_at = now(),
    completed_at = NULL
FROM candidate, projects AS p
WHERE c.id = candidate.id
  AND p.id = c.project_id
RETURNING c.id, c.project_id, c.requested_by_user_id, c.config_snapshot, p.base_url;


-- name: ClaimNextQueuedCrawlAuto :one
WITH candidate AS (
    SELECT c.id
    FROM crawls AS c
    WHERE c.status = 'queued'
      AND c.source = 'auto'
      AND NOT EXISTS (
          SELECT 1
          FROM crawls AS running
          WHERE running.status = 'running'
            AND running.project_id = c.project_id
      )
    ORDER BY c.created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE crawls AS c
SET status = 'running',
    started_at = now(),
    completed_at = NULL
FROM candidate, projects AS p
WHERE c.id = candidate.id
  AND p.id = c.project_id
RETURNING c.id, c.project_id, c.requested_by_user_id, c.config_snapshot, p.base_url;


-- name: UpdateCrawlScores :exec
UPDATE crawls
SET seo_score = $2,
    aeo_score = $3,
    pagespeed_score = $4,
    overall_score = $5
WHERE id = $1;

-- name: UpdateCrawlGooglePSIResults :exec
UPDATE crawls
SET google_psi_results = $2
WHERE id = $1;

-- name: GetCrawlOrgID :one
SELECT p.organization_id
FROM crawls c
INNER JOIN projects p ON p.id = c.project_id
WHERE c.id = $1
LIMIT 1;

-- name: ListActiveCrawlsForOrganization :many
SELECT
    c.id,
    c.project_id,
    c.status,
    c.urls_discovered,
    c.urls_crawled,
    c.created_at
FROM crawls AS c
INNER JOIN projects AS p ON p.id = c.project_id
WHERE p.organization_id = $1
  AND c.status IN ('queued', 'running')
ORDER BY c.created_at DESC;
