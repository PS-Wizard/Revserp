-- name: CreateCrawlLink :one
INSERT INTO crawl_links (
    crawl_id,
    source_url,
    target_url,
    anchor_text,
    is_internal,
    target_status,
    nofollow
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7
)
RETURNING id, crawl_id, source_url, target_url, anchor_text, is_internal, target_status, nofollow, created_at;

-- name: GetCrawlLinkByIDForUser :one
SELECT
    cl.id,
    cl.crawl_id,
    cl.source_url,
    cl.target_url,
    cl.anchor_text,
    cl.is_internal,
    cl.target_status,
    cl.nofollow,
    cl.created_at
FROM crawl_links AS cl
INNER JOIN crawls AS c ON c.id = cl.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cl.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: CountCrawlLinksForCrawlByUser :one
SELECT COUNT(*)
FROM crawl_links AS cl
INNER JOIN crawls AS c ON c.id = cl.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cl.crawl_id = $1
  AND om.user_id = $2;

-- name: ListCrawlLinksForCrawlByUser :many
SELECT
    cl.id,
    cl.crawl_id,
    cl.source_url,
    cl.target_url,
    cl.anchor_text,
    cl.is_internal,
    cl.target_status,
    cl.nofollow,
    cl.created_at
FROM crawl_links AS cl
INNER JOIN crawls AS c ON c.id = cl.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cl.crawl_id = $1
  AND om.user_id = $2
ORDER BY cl.created_at ASC
LIMIT $3
OFFSET $4;


-- name: ListInternalCrawlLinksForCrawl :many
SELECT
    id,
    crawl_id,
    source_url,
    target_url,
    anchor_text,
    is_internal,
    target_status,
    nofollow,
    created_at
FROM crawl_links
WHERE crawl_id = $1
  AND is_internal = TRUE
ORDER BY created_at ASC;
