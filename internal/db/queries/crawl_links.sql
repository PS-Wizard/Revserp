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
    -- target_status is a fact about THIS crawl's pages, not about the link.
    -- A reused link is reused because its source page answered 304, but its
    -- target may have changed status since, and the resolver only fills NULLs.
    -- Copying the baseline value would freeze a stale status that nothing ever
    -- corrects, so broken-target issues would silently vanish. Leave it NULL
    -- for this crawl to resolve.
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

-- name: ResolveInternalLinkTargetStatusesBatch :execrows
-- Fills target_status for one bounded slice of this crawl's internal links
-- from the pages actually crawled. The caller loops until a pass affects no
-- rows, so a single slow moment cannot exceed DB_STATEMENT_TIMEOUT and lose
-- the whole resolution. Rows whose target was never crawled are excluded by
-- the join, so they cannot stall the loop.
--
-- DISTINCT ON is load bearing. crawl_pages only enforces UNIQUE (crawl_id,
-- url), so several crawled pages can share one normalized key, and a plain
-- join returns the same link once per matching page. The caller decides to
-- stop from the affected row count, so that fan-out would read as "no work
-- left" while eligible links remain. Ordering by status_code also picks the
-- same page on every run instead of leaving the choice to the planner.
--
-- The normalization is the one the previous inline join used: fragment
-- stripped, the whole URL lowercased, trailing slashes trimmed. That trims
-- the bare root slash too, so https://example.com/ and https://example.com
-- share a key, and it lowercases the path, which normalizeGraphURL in
-- internal/competitorgaps does not. Both predate this query.
WITH batch AS (
    SELECT DISTINCT ON (cl.id) cl.id, cp.status_code
    FROM crawl_links AS cl
    INNER JOIN crawl_pages AS cp
        ON cp.crawl_id = cl.crawl_id
       AND cp.url_key = cl.target_url_key
    WHERE cl.crawl_id = sqlc.arg(crawl_id)
      AND cl.is_internal = TRUE
      AND cl.target_status IS NULL
      AND cp.status_code IS NOT NULL
    ORDER BY cl.id, cp.status_code
    LIMIT sqlc.arg(batch_size)
)
UPDATE crawl_links AS cl
SET target_status = batch.status_code
FROM batch
WHERE cl.id = batch.id;
