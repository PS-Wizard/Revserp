-- name: CreateCrawlIssue :one
INSERT INTO crawl_issues (
    crawl_id,
    crawl_page_id,
    url,
    severity,
    category,
    code,
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
    $8
)
RETURNING id, crawl_id, crawl_page_id, url, severity, category, code, message, details, created_at;

-- name: GetCrawlIssueByIDForUser :one
SELECT
    ci.id,
    ci.crawl_id,
    ci.crawl_page_id,
    ci.url,
    ci.severity,
    ci.category,
    ci.code,
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

-- name: ListCrawlIssuesForCrawlByUser :many
SELECT
    ci.id,
    ci.crawl_id,
    ci.crawl_page_id,
    ci.url,
    ci.severity,
    ci.category,
    ci.code,
    ci.message,
    ci.details,
    ci.created_at
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.crawl_id = $1
  AND om.user_id = $2
ORDER BY ci.created_at ASC;
