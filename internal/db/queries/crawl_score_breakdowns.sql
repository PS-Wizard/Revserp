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

-- name: GetCrawlScoreBreakdownByCrawl :one
SELECT
    csb.crawl_id,
    csb.scoring_version,
    csb.breakdown_json,
    csb.created_at,
    csb.updated_at
FROM crawl_score_breakdowns AS csb
WHERE csb.crawl_id = $1
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
        ci.details,
        ci.issue_group_id
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
observed_current_pages AS (
    -- Pages the current crawl actually observed with usable evidence.
    -- A baseline URL without such a page cannot be classified as resolved.
    SELECT DISTINCT cp.url
    FROM crawl_pages AS cp
    WHERE cp.crawl_id = $2
      AND cp.soft_404 = FALSE
      AND (cp.fetch_error IS NULL OR cp.fetch_error = '')
      AND cp.status_code BETWEEN 200 AND 299
),
current_crawl_evidence AS (
    SELECT COALESCE(c.google_psi_results #>> '{0,mobile,success}', 'false') = 'true' AS psi_observed
    FROM crawls AS c
    WHERE c.id = $2
),
diff_urls AS (
    SELECT
        CASE
            WHEN baseline_urls.url IS NULL THEN 'new'
            WHEN current_urls.url IS NOT NULL THEN
                CASE
                    WHEN current_urls.severity_rank < baseline_urls.severity_rank THEN 'improved'
                    WHEN current_urls.severity_rank > baseline_urls.severity_rank THEN 'regressed'
                    WHEN baseline_urls.message <> current_urls.message
                      OR baseline_urls.details <> current_urls.details THEN 'changed'
                    ELSE 'unchanged'
                END
            WHEN current_crawl_evidence.psi_observed IS FALSE AND $5 = 'psi_cwv' THEN 'not_verified'
            WHEN $6 IN ('weak_open_graph_coverage', 'missing_website_schema', 'missing_org_identity_schema', 'missing_about_page', 'missing_contact_page', 'missing_policy_page', 'homepage_missing_org_contact_trust_signals') AND EXISTS (
			 SELECT 1 FROM crawl_pages AS baseline_coverage
			 WHERE baseline_coverage.crawl_id = $1 AND baseline_coverage.fetch_error IS NULL AND baseline_coverage.soft_404 = FALSE AND baseline_coverage.status_code BETWEEN 200 AND 299
			   AND NOT EXISTS (SELECT 1 FROM crawl_pages AS current_coverage WHERE current_coverage.crawl_id = $2 AND current_coverage.url = baseline_coverage.url AND current_coverage.fetch_error IS NULL AND current_coverage.soft_404 = FALSE AND current_coverage.status_code BETWEEN 200 AND 299)
			 ) THEN 'not_verified'
            WHEN observed_current_pages.url IS NULL THEN 'not_verified'
            WHEN baseline_urls.issue_group_id IS NOT NULL AND EXISTS (
                SELECT 1 FROM crawl_issue_group_members AS member
                WHERE member.group_id = baseline_urls.issue_group_id
                  AND NOT EXISTS (
                      SELECT 1 FROM crawl_pages AS member_page
                      WHERE member_page.crawl_id = $2 AND member_page.url = member.url
                        AND member_page.fetch_error IS NULL AND member_page.soft_404 = FALSE
                        AND member_page.status_code BETWEEN 200 AND 299
                  )
            ) THEN 'not_verified'
            ELSE 'no_longer_detected'
        END AS change_type
    FROM baseline_urls
    FULL OUTER JOIN current_urls ON current_urls.url = baseline_urls.url
    LEFT JOIN observed_current_pages ON observed_current_pages.url = baseline_urls.url
    CROSS JOIN current_crawl_evidence
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
WHERE $7 = '' OR change_type = $7 OR contribution_type = $7
   OR ($7 = 'resolved' AND change_type = 'no_longer_detected');

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
        ci.details,
        ci.issue_group_id
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
observed_current_pages AS (
    -- Pages the current crawl actually observed with usable evidence.
    -- A baseline URL without such a page cannot be classified as resolved.
    SELECT DISTINCT cp.url
    FROM crawl_pages AS cp
    WHERE cp.crawl_id = $2
      AND cp.soft_404 = FALSE
      AND (cp.fetch_error IS NULL OR cp.fetch_error = '')
      AND cp.status_code BETWEEN 200 AND 299
),
current_crawl_evidence AS (
    SELECT COALESCE(c.google_psi_results #>> '{0,mobile,success}', 'false') = 'true' AS psi_observed
    FROM crawls AS c
    WHERE c.id = $2
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
            WHEN current_urls.url IS NOT NULL THEN
                CASE
                    WHEN current_urls.severity_rank < baseline_urls.severity_rank THEN 'improved'
                    WHEN current_urls.severity_rank > baseline_urls.severity_rank THEN 'regressed'
                    WHEN baseline_urls.message <> current_urls.message
                      OR baseline_urls.details <> current_urls.details THEN 'changed'
                    ELSE 'unchanged'
                END
            WHEN current_crawl_evidence.psi_observed IS FALSE AND $5 = 'psi_cwv' THEN 'not_verified'
            WHEN $6 IN ('weak_open_graph_coverage', 'missing_website_schema', 'missing_org_identity_schema', 'missing_about_page', 'missing_contact_page', 'missing_policy_page', 'homepage_missing_org_contact_trust_signals') AND EXISTS (
			 SELECT 1 FROM crawl_pages AS baseline_coverage
			 WHERE baseline_coverage.crawl_id = $1 AND baseline_coverage.fetch_error IS NULL AND baseline_coverage.soft_404 = FALSE AND baseline_coverage.status_code BETWEEN 200 AND 299
			   AND NOT EXISTS (SELECT 1 FROM crawl_pages AS current_coverage WHERE current_coverage.crawl_id = $2 AND current_coverage.url = baseline_coverage.url AND current_coverage.fetch_error IS NULL AND current_coverage.soft_404 = FALSE AND current_coverage.status_code BETWEEN 200 AND 299)
			 ) THEN 'not_verified'
            WHEN observed_current_pages.url IS NULL THEN 'not_verified'
            WHEN baseline_urls.issue_group_id IS NOT NULL AND EXISTS (
                SELECT 1 FROM crawl_issue_group_members AS member
                WHERE member.group_id = baseline_urls.issue_group_id
                  AND NOT EXISTS (
                      SELECT 1 FROM crawl_pages AS member_page
                      WHERE member_page.crawl_id = $2 AND member_page.url = member.url
                        AND member_page.fetch_error IS NULL AND member_page.soft_404 = FALSE
                        AND member_page.status_code BETWEEN 200 AND 299
                  )
            ) THEN 'not_verified'
            ELSE 'no_longer_detected'
        END AS change_type
    FROM baseline_urls
    FULL OUTER JOIN current_urls ON current_urls.url = baseline_urls.url
    LEFT JOIN observed_current_pages ON observed_current_pages.url = baseline_urls.url
    CROSS JOIN current_crawl_evidence
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
   OR ($7 = 'resolved' AND change_type = 'no_longer_detected')
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
