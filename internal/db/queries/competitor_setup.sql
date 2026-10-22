-- name: GetCompetitorSetupByCompetitor :one
SELECT
    id,
    organization_id,
    project_id,
    competitor_id,
    requested_by_user_id,
    parent_crawl_id,
    crawl_id,
    status,
    crawl_status,
    keyword_generation_status,
    keyword_analysis_status,
    error,
    failed_step,
    step_reasons,
    created_at,
    updated_at,
    completed_at
FROM competitor_setup
WHERE competitor_id = $1
LIMIT 1;

-- name: GetCompetitorSetupByCompetitorForUpdate :one
SELECT
    id,
    organization_id,
    project_id,
    competitor_id,
    requested_by_user_id,
    parent_crawl_id,
    crawl_id,
    status,
    crawl_status,
    keyword_generation_status,
    keyword_analysis_status,
    error,
    failed_step,
    step_reasons,
    created_at,
    updated_at,
    completed_at
FROM competitor_setup
WHERE competitor_id = $1
LIMIT 1
FOR UPDATE;

-- name: CreateCompetitorSetup :one
INSERT INTO competitor_setup (
    organization_id,
    project_id,
    competitor_id,
    requested_by_user_id,
    parent_crawl_id,
    crawl_id,
    status,
    crawl_status,
    keyword_generation_status,
    keyword_analysis_status
) VALUES (
    sqlc.arg(organization_id),
    sqlc.arg(project_id),
    sqlc.arg(competitor_id),
    sqlc.arg(requested_by_user_id),
    sqlc.arg(parent_crawl_id),
    sqlc.arg(crawl_id),
    sqlc.arg(status),
    sqlc.arg(crawl_status),
    sqlc.arg(keyword_generation_status),
    sqlc.arg(keyword_analysis_status)
)
ON CONFLICT (competitor_id) DO NOTHING
RETURNING id, organization_id, project_id, competitor_id, requested_by_user_id, parent_crawl_id, crawl_id, status, crawl_status, keyword_generation_status, keyword_analysis_status, error, failed_step, step_reasons, created_at, updated_at, completed_at;

-- name: UpdateCompetitorSetup :one
UPDATE competitor_setup
SET requested_by_user_id = COALESCE(sqlc.narg(requested_by_user_id), requested_by_user_id),
    parent_crawl_id = COALESCE(sqlc.narg(parent_crawl_id), parent_crawl_id),
    crawl_id = COALESCE(sqlc.narg(crawl_id), crawl_id),
    status = sqlc.arg(status),
    crawl_status = sqlc.arg(crawl_status),
    keyword_generation_status = sqlc.arg(keyword_generation_status),
    keyword_analysis_status = sqlc.arg(keyword_analysis_status),
    error = sqlc.narg(error),
    failed_step = sqlc.narg(failed_step),
    step_reasons = sqlc.arg(step_reasons),
    updated_at = now(),
    completed_at = CASE WHEN sqlc.arg(status) IN ('completed', 'failed') THEN now() ELSE NULL END
WHERE competitor_id = sqlc.arg(competitor_id)
RETURNING id, organization_id, project_id, competitor_id, requested_by_user_id, parent_crawl_id, crawl_id, status, crawl_status, keyword_generation_status, keyword_analysis_status, error, failed_step, step_reasons, created_at, updated_at, completed_at;

-- name: GetCompetitorChildCrawlForParent :one
SELECT id, status
FROM crawls
WHERE parent_crawl_id = sqlc.arg(parent_crawl_id)
  AND competitor_id = sqlc.arg(competitor_id)
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: DeleteFailedCompetitorCrawlForParent :exec
DELETE FROM crawls
WHERE parent_crawl_id = sqlc.arg(parent_crawl_id)
  AND competitor_id = sqlc.arg(competitor_id)
  AND source = 'competitor'
  AND status IN ('failed', 'cancelled');

-- name: ListKeywordAnalysisPagesForCrawl :many
SELECT
    url,
    title,
    h1,
    meta_description,
    visible_text,
    word_count
FROM crawl_pages
WHERE crawl_id = $1
  AND is_internal
  AND (fetch_error IS NULL OR fetch_error = '')
ORDER BY word_count DESC NULLS LAST, created_at ASC
LIMIT $2;

-- name: GetProjectCompetitorByID :one
SELECT id, project_id, seed_url, name
FROM project_competitors
WHERE id = $1
LIMIT 1;

-- name: ListCompetitorSetupsCrawlRunning :many
SELECT competitor_id
FROM competitor_setup
WHERE status = 'running' AND crawl_status = 'running';
