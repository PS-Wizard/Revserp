-- name: ListComparisonExcludedCandidateURLs :many
-- Callers filter these candidates through the current crawl's exclusion
-- matcher; URLs the current crawl actually saw are never candidates here.
SELECT DISTINCT
    ci.pillar,
    ci.bucket,
    ci.issue_type,
    ci.url
FROM crawl_issues AS ci
INNER JOIN crawls AS baseline ON baseline.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = baseline.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = sqlc.arg(baseline_crawl_id)
  AND om.user_id = sqlc.arg(user_id)
  AND (sqlc.arg(pillar)::text = '' OR ci.pillar = sqlc.arg(pillar)::text)
  AND (sqlc.arg(bucket)::text = '' OR ci.bucket = sqlc.arg(bucket)::text)
  AND (sqlc.arg(issue_type)::text = '' OR ci.issue_type = sqlc.arg(issue_type)::text)
  AND NOT EXISTS (
    SELECT 1
    FROM crawl_pages AS cp
    WHERE cp.crawl_id = sqlc.arg(current_crawl_id)
      AND cp.url = ci.url
  )
  AND NOT EXISTS (
    SELECT 1
    FROM crawl_issues AS cur
    WHERE cur.crawl_id = sqlc.arg(current_crawl_id)
      AND cur.pillar = ci.pillar
      AND cur.bucket = ci.bucket
      AND cur.issue_type = ci.issue_type
      AND cur.url = ci.url
  )
ORDER BY ci.pillar, ci.bucket, ci.issue_type, ci.url;
