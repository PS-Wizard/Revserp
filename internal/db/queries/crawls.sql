-- name: CreateCrawl :one
INSERT INTO crawls (
    project_id,
    status,
    config_snapshot,
    started_at
) VALUES (
    $1,
    $2,
    $3,
    $4
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
ORDER BY created_at DESC;

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
