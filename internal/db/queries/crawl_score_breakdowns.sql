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

-- name: CountCompareCrawlIssueURLsByTypeForCrawlByUser :one
WITH baseline_urls AS (
    SELECT DISTINCT ON (ci.url)
        ci.url,
        ci.severity,
        CASE ci.severity
            WHEN 'critical' THEN 4
            WHEN 'high' THEN 3
            WHEN 'medium' THEN 2
            WHEN 'low' THEN 1
            ELSE 0
        END AS severity_rank,
        ci.message,
        ci.details
    FROM crawl_issues AS ci
    INNER JOIN crawls AS c ON c.id = ci.crawl_id
    INNER JOIN projects AS p ON p.id = c.project_id
    INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    WHERE ci.crawl_id = $1
      AND om.user_id = $3
      AND ci.pillar = $4
      AND ci.bucket = $5
      AND ci.issue_type = $6
    ORDER BY ci.url ASC, ci.created_at ASC
),
current_urls AS (
    SELECT DISTINCT ON (ci.url)
        ci.url,
        ci.severity,
        CASE ci.severity
            WHEN 'critical' THEN 4
            WHEN 'high' THEN 3
            WHEN 'medium' THEN 2
            WHEN 'low' THEN 1
            ELSE 0
        END AS severity_rank,
        ci.message,
        ci.details
    FROM crawl_issues AS ci
    INNER JOIN crawls AS c ON c.id = ci.crawl_id
    INNER JOIN projects AS p ON p.id = c.project_id
    INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    WHERE ci.crawl_id = $2
      AND om.user_id = $3
      AND ci.pillar = $4
      AND ci.bucket = $5
      AND ci.issue_type = $6
    ORDER BY ci.url ASC, ci.created_at ASC
),
diff_urls AS (
    SELECT
        CASE
            WHEN baseline_urls.url IS NULL THEN 'new'
            WHEN current_urls.url IS NULL THEN 'resolved'
            WHEN current_urls.severity_rank < baseline_urls.severity_rank THEN 'improved'
            WHEN current_urls.severity_rank > baseline_urls.severity_rank THEN 'regressed'
            WHEN baseline_urls.message <> current_urls.message
              OR baseline_urls.details <> current_urls.details THEN 'changed'
            ELSE 'unchanged'
        END AS change_type
    FROM baseline_urls
    FULL OUTER JOIN current_urls ON current_urls.url = baseline_urls.url
),
ranked_urls AS (
    SELECT
        change_type,
        CASE
            WHEN change_type IN ('regressed', 'new') THEN 'regressed'
            WHEN change_type IN ('improved', 'resolved') THEN 'improved'
            ELSE change_type
        END AS contribution_type
    FROM diff_urls
)
SELECT COUNT(*)
FROM ranked_urls
WHERE $7 = '' OR change_type = $7 OR contribution_type = $7;

-- name: ListCompareCrawlIssueURLsByTypeForCrawlByUser :many
WITH baseline_urls AS (
    SELECT DISTINCT ON (ci.url)
        ci.url,
        ci.crawl_page_id,
        ci.severity,
        CASE ci.severity
            WHEN 'critical' THEN 4
            WHEN 'high' THEN 3
            WHEN 'medium' THEN 2
            WHEN 'low' THEN 1
            ELSE 0
        END AS severity_rank,
        ci.message,
        ci.details
    FROM crawl_issues AS ci
    INNER JOIN crawls AS c ON c.id = ci.crawl_id
    INNER JOIN projects AS p ON p.id = c.project_id
    INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    WHERE ci.crawl_id = $1
      AND om.user_id = $3
      AND ci.pillar = $4
      AND ci.bucket = $5
      AND ci.issue_type = $6
    ORDER BY ci.url ASC, ci.created_at ASC
),
current_urls AS (
    SELECT DISTINCT ON (ci.url)
        ci.url,
        ci.crawl_page_id,
        ci.severity,
        CASE ci.severity
            WHEN 'critical' THEN 4
            WHEN 'high' THEN 3
            WHEN 'medium' THEN 2
            WHEN 'low' THEN 1
            ELSE 0
        END AS severity_rank,
        ci.message,
        ci.details
    FROM crawl_issues AS ci
    INNER JOIN crawls AS c ON c.id = ci.crawl_id
    INNER JOIN projects AS p ON p.id = c.project_id
    INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    WHERE ci.crawl_id = $2
      AND om.user_id = $3
      AND ci.pillar = $4
      AND ci.bucket = $5
      AND ci.issue_type = $6
    ORDER BY ci.url ASC, ci.created_at ASC
),
diff_urls AS (
    SELECT
        COALESCE(baseline_urls.url, current_urls.url) AS url,
        baseline_urls.crawl_page_id AS baseline_crawl_page_id,
        baseline_urls.severity AS baseline_severity,
        baseline_urls.message AS baseline_message,
        baseline_urls.details AS baseline_details,
        current_urls.crawl_page_id AS current_crawl_page_id,
        current_urls.severity AS current_severity,
        current_urls.message AS current_message,
        current_urls.details AS current_details,
        CASE
            WHEN baseline_urls.url IS NULL THEN 'new'
            WHEN current_urls.url IS NULL THEN 'resolved'
            WHEN current_urls.severity_rank < baseline_urls.severity_rank THEN 'improved'
            WHEN current_urls.severity_rank > baseline_urls.severity_rank THEN 'regressed'
            WHEN baseline_urls.message <> current_urls.message
              OR baseline_urls.details <> current_urls.details THEN 'changed'
            ELSE 'unchanged'
        END AS change_type
    FROM baseline_urls
    FULL OUTER JOIN current_urls ON current_urls.url = baseline_urls.url
),
ranked_urls AS (
    SELECT
        *,
        CASE
            WHEN change_type IN ('regressed', 'new') THEN 'regressed'
            WHEN change_type IN ('improved', 'resolved') THEN 'improved'
            ELSE change_type
        END AS contribution_type
    FROM diff_urls
)
SELECT
    url,
    baseline_crawl_page_id,
    baseline_severity,
    baseline_message,
    baseline_details,
    current_crawl_page_id,
    current_severity,
    current_message,
    current_details,
    change_type
FROM ranked_urls
WHERE $7 = '' OR change_type = $7 OR contribution_type = $7
ORDER BY url ASC
LIMIT $8
OFFSET $9;

-- name: ListCompletedProjectCrawlScoreBreakdownsForUser :many
SELECT
    c.id AS crawl_id,
    c.created_at,
    c.completed_at,
    csb.breakdown_json
FROM crawl_score_breakdowns AS csb
INNER JOIN crawls AS c ON c.id = csb.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE c.project_id = $1
  AND om.user_id = $2
  AND c.status = 'completed'
ORDER BY c.created_at DESC
LIMIT $3;
