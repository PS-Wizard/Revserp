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
    COALESCE(
        cl.target_status,
        CASE WHEN cl.is_internal IS TRUE THEN (
            SELECT MIN(cp.status_code)
            FROM crawl_pages AS cp
            WHERE cp.crawl_id = cl.crawl_id
              AND digest(cp.url_key, 'sha256') = digest(cl.target_url_key, 'sha256')
              AND cp.url_key = cl.target_url_key
              AND cp.status_code IS NOT NULL
        ) END
    ) AS target_status,
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
WITH page AS MATERIALIZED (
    SELECT
        cl.id,
        cl.crawl_id,
        cl.source_url,
        cl.target_url,
        cl.anchor_text,
        cl.is_internal,
        cl.target_status,
        cl.target_url_key,
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
    OFFSET $4
)
SELECT
    page.id,
    page.crawl_id,
    page.source_url,
    page.target_url,
    page.anchor_text,
    page.is_internal,
    COALESCE(
        page.target_status,
        CASE WHEN page.is_internal IS TRUE THEN (
            SELECT MIN(cp.status_code)
            FROM crawl_pages AS cp
            WHERE cp.crawl_id = page.crawl_id
              AND digest(cp.url_key, 'sha256') = digest(page.target_url_key, 'sha256')
              AND cp.url_key = page.target_url_key
              AND cp.status_code IS NOT NULL
        ) END
    ) AS target_status,
    page.nofollow,
    page.created_at
FROM page
ORDER BY page.created_at ASC;


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

-- name: CopyCrawlLinksFromBaseline :execrows
INSERT INTO crawl_links (
    crawl_id,
    source_url,
    target_url,
    anchor_text,
    is_internal,
    target_status,
    nofollow
)
SELECT
    sqlc.arg(crawl_id),
    source_url,
    target_url,
    anchor_text,
    is_internal,
    NULL,
    nofollow
FROM crawl_links
WHERE crawl_links.crawl_id = sqlc.arg(baseline_crawl_id)
  AND crawl_links.source_url = sqlc.arg(source_url)
ON CONFLICT DO NOTHING;

-- name: ListInternalLinkPairsForCrawl :many
SELECT source_url, target_url
FROM crawl_links
WHERE crawl_id = sqlc.arg(crawl_id)
  AND is_internal = TRUE;

-- name: ListInternalLinkFactsForCrawl :many
SELECT source_url, target_url, target_url_key, target_status
FROM crawl_links
WHERE crawl_id = $1
  AND is_internal = TRUE
ORDER BY created_at ASC;
