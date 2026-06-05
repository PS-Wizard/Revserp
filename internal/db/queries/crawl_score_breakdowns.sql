-- name: UpsertCrawlScoreBreakdown :exec
INSERT INTO crawl_score_breakdowns (
    crawl_id,
    scoring_version,
    breakdown_json
) VALUES (
    $1,
    $2,
    $3
)
ON CONFLICT (crawl_id) DO UPDATE SET
    scoring_version = EXCLUDED.scoring_version,
    breakdown_json = EXCLUDED.breakdown_json,
    updated_at = now();

-- name: GetCrawlScoreBreakdownByCrawlForUser :one
SELECT
    csb.crawl_id,
    csb.scoring_version,
    csb.breakdown_json,
    csb.created_at,
    csb.updated_at
FROM crawl_score_breakdowns AS csb
INNER JOIN crawls AS c ON c.id = csb.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE csb.crawl_id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: CountDistinctCrawlIssueURLsByTypeForCrawlByUser :one
SELECT COUNT(DISTINCT ci.url)
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
  AND ci.pillar = $3
  AND ci.bucket = $4
  AND ci.issue_type = $5;

-- name: ListDistinctCrawlIssueURLsByTypeForCrawlByUser :many
SELECT DISTINCT ON (ci.url)
    ci.url,
    ci.crawl_page_id,
    ci.severity,
    ci.message,
    ci.details
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
  AND ci.pillar = $3
  AND ci.bucket = $4
  AND ci.issue_type = $5
ORDER BY
    ci.url ASC,
    CASE ci.severity
        WHEN 'high' THEN 3
        WHEN 'medium' THEN 2
        WHEN 'low' THEN 1
        ELSE 0
    END DESC,
    ci.created_at ASC
LIMIT $6
OFFSET $7;
