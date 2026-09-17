-- name: ListProjectCompetitorsForUser :many
SELECT
    pc.id,
    pc.project_id,
    pc.seed_url,
    pc.name,
    pc.created_at
FROM project_competitors AS pc
INNER JOIN projects AS p ON p.id = pc.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
WHERE pc.project_id = sqlc.arg(project_id)
ORDER BY pc.created_at ASC;

-- name: ListProjectCompetitorsWithCrawlForUser :many
SELECT
    pc.id,
    pc.project_id,
    pc.seed_url,
    pc.name,
    pc.created_at,
    c.id AS crawl_id,
    c.status AS crawl_status,
    c.phase AS crawl_phase,
    c.config_snapshot AS crawl_config_snapshot,
    c.urls_discovered AS crawl_urls_discovered,
    c.urls_crawled AS crawl_urls_crawled,
    c.max_depth_reached AS crawl_max_depth_reached,
    c.google_psi_results AS crawl_google_psi_results,
    c.has_llms_txt AS crawl_has_llms_txt,
    c.seo_score AS crawl_seo_score,
    c.aeo_score AS crawl_aeo_score,
    c.pagespeed_score AS crawl_pagespeed_score,
    c.overall_score AS crawl_overall_score,
    c.started_at AS crawl_started_at,
    c.completed_at AS crawl_completed_at,
    c.created_at AS crawl_created_at,
    c.parent_crawl_id AS crawl_parent_crawl_id,
    c.competitor_id AS crawl_competitor_id
FROM project_competitors AS pc
INNER JOIN projects AS p ON p.id = pc.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
LEFT JOIN crawls AS c ON c.competitor_id = pc.id
    AND c.parent_crawl_id = sqlc.arg(parent_crawl_id)
WHERE pc.project_id = sqlc.arg(project_id)
ORDER BY pc.created_at ASC;

-- name: InsertProjectCompetitor :one
INSERT INTO project_competitors (
    project_id,
    seed_url,
    name
) VALUES (
    sqlc.arg(project_id),
    sqlc.arg(seed_url),
    sqlc.arg(name)
)
RETURNING id, project_id, seed_url, name, created_at;

-- name: DeleteProjectCompetitorForUser :execrows
DELETE FROM project_competitors AS pc
USING projects AS p, organization_members AS om
WHERE pc.id = sqlc.arg(competitor_id)
  AND pc.project_id = p.id
  AND p.organization_id = om.org_id
  AND om.user_id = sqlc.arg(user_id)
  AND pc.project_id = sqlc.arg(project_id);

-- name: CountProjectCompetitors :one
SELECT COUNT(*)::bigint
FROM project_competitors
WHERE project_id = sqlc.arg(project_id);

-- name: GetProjectCompetitorByIDForUser :one
SELECT
    pc.id,
    pc.project_id,
    pc.seed_url,
    pc.name,
    pc.created_at
FROM project_competitors AS pc
INNER JOIN projects AS p ON p.id = pc.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
WHERE pc.id = sqlc.arg(competitor_id)
  AND pc.project_id = sqlc.arg(project_id)
LIMIT 1;

-- name: ListProjectCompetitorsWithoutChildCrawl :many
SELECT
    pc.id,
    pc.seed_url
FROM project_competitors AS pc
WHERE pc.project_id = sqlc.arg(project_id)
  AND NOT EXISTS (
      SELECT 1
      FROM crawls AS c
      WHERE c.competitor_id = pc.id
        AND c.parent_crawl_id = sqlc.arg(parent_crawl_id)
  )
ORDER BY pc.created_at ASC;

-- name: GetOrganizationMaxCompetitorsByProjectID :one
SELECT COALESCE(f.max_competitors, 3)::integer AS max_competitors
FROM projects AS p
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id);

-- name: DeleteFailedCompetitorCrawlsForParent :exec
DELETE FROM crawls
WHERE parent_crawl_id = sqlc.arg(parent_crawl_id)
  AND source = 'competitor'
  AND status IN ('failed', 'cancelled');
