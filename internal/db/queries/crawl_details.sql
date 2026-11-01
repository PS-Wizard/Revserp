-- Crawl details dialog aggregates: status-code breakdown, failed pages, link totals.
-- All queries aggregate in SQL; callers must gate on GetCrawlByIDForUser first
-- so unknown or foreign crawls never reach them.

-- name: GetCrawlDetailsStatusCodes :many
SELECT
    status_code,
    COUNT(*)::bigint AS count
FROM crawl_pages
WHERE crawl_id = $1
GROUP BY status_code
ORDER BY status_code ASC NULLS LAST;

-- name: CountCrawlDetailsSoft404 :one
SELECT COUNT(*)::bigint
FROM crawl_pages
WHERE crawl_id = $1
  AND soft_404 IS TRUE;

-- name: CountCrawlDetailsFailedPages :one
SELECT COUNT(*)::bigint
FROM crawl_pages
WHERE crawl_id = $1
  AND (status_code >= 400 OR status_code IS NULL OR fetch_error IS NOT NULL);

-- name: ListCrawlDetailsFailedPages :many
SELECT
    url,
    status_code,
    fetch_error
FROM crawl_pages
WHERE crawl_id = $1
  AND (status_code >= 400 OR status_code IS NULL OR fetch_error IS NOT NULL)
ORDER BY (fetch_error IS NOT NULL) DESC, status_code DESC NULLS FIRST, url ASC
LIMIT 100;

-- name: GetCrawlDetailsLinkStats :one
-- Internal links without a stored target_status fall back to the target
-- page's status in the same crawl, like GetCrawlLinkByIDForUser does.
WITH page_status AS (
    SELECT url_key, MIN(status_code) AS status_code
    FROM crawl_pages
    WHERE crawl_id = $1 AND status_code IS NOT NULL
    GROUP BY url_key
)
SELECT
    COUNT(*) FILTER (WHERE cl.is_internal IS TRUE)::bigint AS internal_count,
    COUNT(*) FILTER (WHERE cl.is_internal IS FALSE)::bigint AS external_count,
    COUNT(*) FILTER (WHERE cl.nofollow IS TRUE)::bigint AS nofollow_count,
    COUNT(*) FILTER (
        WHERE cl.is_internal IS TRUE
          AND COALESCE(cl.target_status, ps.status_code) >= 400
    )::bigint AS broken_internal_count,
    COUNT(DISTINCT lower(substring(cl.target_url from '^[a-zA-Z]+://([^/:?#]+)'))) FILTER (WHERE cl.is_internal IS FALSE)::bigint AS unique_external_domains
FROM crawl_links AS cl
LEFT JOIN page_status AS ps
    ON cl.is_internal IS TRUE
   AND cl.target_status IS NULL
   AND ps.url_key = cl.target_url_key
WHERE cl.crawl_id = $1;
