-- name: CreateCrawlIssue :one
INSERT INTO crawl_issues (
    crawl_id,
    crawl_page_id,
    url,
    pillar,
    bucket,
    issue_type,
    severity,
    message,
    details
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9
)
RETURNING id, crawl_id, crawl_page_id, url, pillar, bucket, issue_type, severity, message, details, created_at;

-- name: CreateCrawlIssues :copyfrom
INSERT INTO crawl_issues (
    crawl_id,
    crawl_page_id,
    url,
    pillar,
    bucket,
    issue_type,
    severity,
    message,
    details
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9
);

-- name: GetCrawlIssueByIDForUser :one
SELECT
    ci.id,
    ci.crawl_id,
    ci.crawl_page_id,
    ci.url,
    ci.pillar,
    ci.bucket,
    ci.issue_type,
    ci.severity,
    ci.message,
    ci.details,
    ci.created_at
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: CountCrawlIssuesForCrawlByUser :one
SELECT COUNT(*)
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2;

-- name: ListCrawlIssuesForCrawlByUser :many
SELECT
    ci.id,
    ci.crawl_id,
    ci.crawl_page_id,
    ci.url,
    ci.pillar,
    ci.bucket,
    ci.issue_type,
    ci.severity,
    ci.message,
    ci.details,
    ci.created_at
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
ORDER BY ci.created_at ASC
LIMIT $3
OFFSET $4;

-- name: DeleteCrawlIssuesForCrawl :exec
DELETE FROM crawl_issues
WHERE crawl_id = $1;

-- name: ListCrawlIssuesForCrawl :many
SELECT
    id,
    crawl_id,
    crawl_page_id,
    url,
    pillar,
    bucket,
    issue_type,
    severity,
    message,
    details,
    created_at
FROM crawl_issues
WHERE crawl_id = $1
ORDER BY created_at ASC;

-- name: ListCrawlIssuesFilteredForUser :many
SELECT
    ci.id,
    ci.crawl_id,
    ci.crawl_page_id,
    ci.url,
    ci.pillar,
    ci.bucket,
    ci.issue_type,
    ci.severity,
    ci.message,
    ci.details,
    ci.created_at
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
  AND (coalesce(cardinality($3::text[]), 0) = 0 OR ci.pillar = ANY($3::text[]))
  AND ($4 = '' OR ci.bucket = $4)
  AND ($5 = '' OR ci.issue_type = $5)
  AND ($6 = '' OR ci.severity = $6)
  AND ($7 = '' OR ci.url = $7)
  AND (coalesce(cardinality($8::text[]), 0) = 0 OR ci.url = ANY($8::text[]))
ORDER BY
    row_number() OVER (
        PARTITION BY ci.pillar
        ORDER BY CASE ci.severity WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END DESC,
                 ci.bucket,
                 ci.issue_type,
                 ci.url,
                 ci.id
    ),
    ci.pillar
LIMIT $9
OFFSET $10;

-- name: BreakdownCrawlIssuesFilteredForUser :many
SELECT
    ci.pillar,
    ci.bucket,
    ci.issue_type,
    ci.severity,
    COUNT(*) AS issue_count
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
  AND (coalesce(cardinality($3::text[]), 0) = 0 OR ci.pillar = ANY($3::text[]))
  AND ($4 = '' OR ci.bucket = $4)
  AND ($5 = '' OR ci.issue_type = $5)
  AND ($6 = '' OR ci.severity = $6)
  AND ($7 = '' OR ci.url = $7)
  AND (coalesce(cardinality($8::text[]), 0) = 0 OR ci.url = ANY($8::text[]))
GROUP BY ci.pillar, ci.bucket, ci.issue_type, ci.severity;

-- name: ListDistinctCrawlIssueDimensions :many
SELECT DISTINCT
    ci.pillar,
    ci.bucket,
    ci.issue_type
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
ORDER BY ci.pillar, ci.bucket, ci.issue_type;
-- name: CountCrawlIssuesFilteredForUser :one
SELECT COUNT(*)
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
  AND (coalesce(cardinality($3::text[]), 0) = 0 OR ci.pillar = ANY($3::text[]))
  AND ($4 = '' OR ci.bucket = $4)
  AND ($5 = '' OR ci.issue_type = $5)
  AND ($6 = '' OR ci.severity = $6)
  AND (coalesce(cardinality($7::text[]), 0) = 0 OR ci.url = ANY($7::text[]));
