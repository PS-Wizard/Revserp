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
RETURNING id, project_id, status, config_snapshot, seo_score, aeo_score, pagespeed_score, overall_score, started_at, completed_at, created_at;

-- name: GetCrawlByIDForUser :one
SELECT
    c.id,
    c.project_id,
    c.status,
    c.config_snapshot,
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
