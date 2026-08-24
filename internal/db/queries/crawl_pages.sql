-- name: CreateCrawlPage :one
INSERT INTO crawl_pages (
    crawl_id,
    url,
    status_code,
    content_type,
    size_bytes,
    is_internal,
    depth,
    title,
    meta_description,
    h1,
    h1_count,
    h2_count,
    h3_count,
    word_count,
    visible_text,
    content_sha256,
    author,
    canonical_url,
    lang,
    viewport,
    robots,
    image_count,
    images_without_alt_count,
    images_without_dimensions,
    external_links,
    internal_links,
    response_time_ms,
    javascript_rendered,
    h2_headings,
    h3_headings,
    heading_outline,
    og_tags,
    json_ld,
    content_blocks,
    etag,
    last_modified,
    soft_404,
    fetch_error
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9,
    $10,
    $11,
    $12,
    $13,
    $14,
    $15,
    $16,
    $17,
    $18,
    $19,
    $20,
    $21,
    $22,
    $23,
    $24,
    $25,
    $26,
    $27,
    $28,
    $29,
    $30,
    $31,
    $32,
    $33,
    $34,
    $35,
    $36,
    $37,
    $38
)
RETURNING id, crawl_id, url, status_code, content_type, size_bytes, is_internal, depth, title, meta_description, h1, h1_count, h2_count, h3_count, word_count, visible_text, content_sha256, author, canonical_url, lang, viewport, robots, image_count, images_without_alt_count, images_without_dimensions, external_links, internal_links, response_time_ms, javascript_rendered, h2_headings, h3_headings, heading_outline, og_tags, json_ld, content_blocks, etag, last_modified, soft_404, fetch_error, created_at;

-- name: GetCrawlPageByIDForUser :one
SELECT
    cp.id,
    cp.crawl_id,
    cp.url,
    cp.status_code,
    cp.content_type,
    cp.size_bytes,
    cp.is_internal,
    cp.depth,
    cp.title,
    cp.meta_description,
    cp.h1,
    cp.h1_count,
    cp.h2_count,
    cp.h3_count,
    cp.word_count,
    cp.visible_text,
    cp.content_sha256,
    cp.author,
    cp.canonical_url,
    cp.lang,
    cp.viewport,
    cp.robots,
    cp.image_count,
    cp.images_without_alt_count,
    cp.images_without_dimensions,
    cp.external_links,
    cp.internal_links,
    cp.response_time_ms,
    cp.javascript_rendered,
    cp.h2_headings,
    cp.h3_headings,
    cp.heading_outline,
    cp.og_tags,
    cp.json_ld,
    cp.content_blocks,
    cp.created_at
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.id = $1
  AND om.user_id = $2
LIMIT 1;

-- name: CountCrawlPagesForCrawlByUser :one
SELECT COUNT(*)
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.crawl_id = $1
  AND om.user_id = $2;

-- name: ListCrawlPagesForCrawlByUser :many
SELECT
    cp.id,
    cp.crawl_id,
    cp.url,
    cp.status_code,
    cp.content_type,
    cp.size_bytes,
    cp.is_internal,
    cp.depth,
    cp.title,
    cp.meta_description,
    cp.h1,
    cp.h1_count,
    cp.h2_count,
    cp.h3_count,
    cp.word_count,
    cp.visible_text,
    cp.content_sha256,
    cp.author,
    cp.canonical_url,
    cp.lang,
    cp.viewport,
    cp.robots,
    cp.image_count,
    cp.images_without_alt_count,
    cp.images_without_dimensions,
    cp.external_links,
    cp.internal_links,
    cp.response_time_ms,
    cp.javascript_rendered,
    cp.soft_404,
    cp.fetch_error,
    cp.h2_headings,
    cp.h3_headings,
    cp.heading_outline,
    cp.og_tags,
    cp.json_ld,
    cp.content_blocks,
    cp.created_at
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.crawl_id = $1
  AND om.user_id = $2
ORDER BY cp.created_at ASC
LIMIT $3
OFFSET $4;


-- name: ListCrawlPagesForCrawl :many
SELECT
    id,
    crawl_id,
    url,
    status_code,
    content_type,
    size_bytes,
    is_internal,
    depth,
    title,
    meta_description,
    h1,
    h1_count,
    h2_count,
    h3_count,
    word_count,
    visible_text,
    content_sha256,
    author,
    canonical_url,
    lang,
    viewport,
    robots,
    image_count,
    images_without_alt_count,
    images_without_dimensions,
    external_links,
    internal_links,
    response_time_ms,
    javascript_rendered,
    soft_404,
    fetch_error,
    h2_headings,
    h3_headings,
    heading_outline,
    og_tags,
    json_ld,
    content_blocks,
    created_at
FROM crawl_pages
WHERE crawl_id = $1
ORDER BY created_at ASC;

-- name: UpdateCrawlPageContentFingerprints :exec
UPDATE crawl_pages
SET content_sha256 = $2
WHERE id = $1;

-- name: BulkUpdateCrawlPageContentFingerprints :exec
UPDATE crawl_pages AS cp
SET content_sha256 = NULLIF(data.content_sha256, '')
FROM (
    SELECT unnest($1::uuid[]) AS id, unnest($2::text[]) AS content_sha256
) AS data
WHERE cp.id = data.id;

-- name: GetCrawlPageByURLForUser :one
SELECT
    cp.id,
    cp.crawl_id,
    cp.url,
    cp.status_code,
    cp.content_type,
    cp.size_bytes,
    cp.is_internal,
    cp.depth,
    cp.title,
    cp.meta_description,
    cp.h1,
    cp.h1_count,
    cp.h2_count,
    cp.h3_count,
    cp.word_count,
    cp.visible_text,
    cp.content_sha256,
    cp.author,
    cp.canonical_url,
    cp.lang,
    cp.viewport,
    cp.robots,
    cp.image_count,
    cp.images_without_alt_count,
    cp.images_without_dimensions,
    cp.external_links,
    cp.internal_links,
    cp.response_time_ms,
    cp.javascript_rendered,
    cp.h2_headings,
    cp.h3_headings,
    cp.heading_outline,
    cp.og_tags,
    cp.json_ld,
    cp.content_blocks,
    cp.created_at
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.crawl_id = $1
  AND cp.url = $2
  AND om.user_id = $3
LIMIT 1;

-- name: GetCrawlPageContentByURLForUser :one
-- content_blocks gated by include_content; metadata mode avoids large blob fetch, content_available reports presence.
SELECT
    cp.id,
    cp.crawl_id,
    cp.url,
    cp.title,
    cp.meta_description,
    cp.h1,
    cp.word_count,
    cp.status_code,
    cp.content_type,
    cp.fetch_error,
    (CASE WHEN sqlc.arg(include_content)::boolean THEN cp.content_blocks ELSE NULL END)::jsonb AS content_blocks,
    (CASE
        WHEN sqlc.arg(include_content)::boolean THEN
            cp.content_blocks IS NOT NULL
            AND jsonb_typeof(cp.content_blocks) = 'array'
            AND jsonb_array_length(cp.content_blocks) > 0
        ELSE cp.content_blocks IS NOT NULL
    END)::boolean AS content_available,
    cp.created_at
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.crawl_id = sqlc.arg(crawl_id)
  AND cp.url = sqlc.arg(url)
  AND om.user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: ListCrawlPagesFilteredForUser :many
SELECT
    cp.url,
    cp.title,
    cp.word_count
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.crawl_id = $1
  AND om.user_id = $2
  AND ($3 = '' OR cp.url ILIKE '%' || $3 || '%')
ORDER BY cp.url ASC
LIMIT $4;

-- name: CountCrawlPagesFilteredForUser :one
SELECT COUNT(*)
FROM crawl_pages AS cp
INNER JOIN crawls AS c ON c.id = cp.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE cp.crawl_id = $1
  AND om.user_id = $2
  AND ($3 = '' OR cp.url ILIKE '%' || $3 || '%');

-- name: GetLatestCompletedCrawlIDForProject :one
SELECT id
FROM crawls
WHERE project_id = sqlc.arg(project_id)
  AND status = 'completed'
  AND id <> sqlc.arg(exclude_crawl_id)
ORDER BY completed_at DESC NULLS LAST
LIMIT 1;

-- name: ListPageValidatorsForCrawl :many
SELECT url, etag, last_modified
FROM crawl_pages
WHERE crawl_id = sqlc.arg(crawl_id)
  AND status_code BETWEEN 200 AND 299
  AND (etag IS NOT NULL OR last_modified IS NOT NULL);

-- name: CopyCrawlPageFromBaseline :execrows
INSERT INTO crawl_pages (
    crawl_id,
    url,
    status_code,
    content_type,
    size_bytes,
    is_internal,
    depth,
    title,
    meta_description,
    h1,
    h1_count,
    h2_count,
    h3_count,
    word_count,
    visible_text,
    content_sha256,
    author,
    canonical_url,
    lang,
    viewport,
    robots,
    image_count,
    images_without_alt_count,
    images_without_dimensions,
    external_links,
    internal_links,
    response_time_ms,
    javascript_rendered,
    h2_headings,
    h3_headings,
    heading_outline,
    og_tags,
    json_ld,
    content_blocks,
    etag,
    last_modified,
    soft_404,
    fetch_error
)
SELECT
    sqlc.arg(crawl_id),
    url,
    status_code,
    content_type,
    size_bytes,
    is_internal,
    sqlc.arg(depth),
    title,
    meta_description,
    h1,
    h1_count,
    h2_count,
    h3_count,
    word_count,
    visible_text,
    content_sha256,
    author,
    canonical_url,
    lang,
    viewport,
    robots,
    image_count,
    images_without_alt_count,
    images_without_dimensions,
    external_links,
    internal_links,
    response_time_ms,
    javascript_rendered,
    h2_headings,
    h3_headings,
    heading_outline,
    og_tags,
    json_ld,
    content_blocks,
    COALESCE(sqlc.narg(fresh_etag)::text, crawl_pages.etag),
    last_modified,
    -- A 304 means the body is unchanged, so a soft 404 stays a soft 404. The
    -- fetch itself succeeded, so no fetch error is carried forward.
    soft_404,
    NULL
FROM crawl_pages
WHERE crawl_pages.crawl_id = sqlc.arg(baseline_crawl_id)
  AND crawl_pages.url = sqlc.arg(url)
ON CONFLICT (crawl_id, url) DO NOTHING;

-- name: GetCrawlPageIssueHistogramForUser :many
-- Distribution of scoreable pages by how many issues each carries, capped at 20+.
-- Mirrors the scoreable-content-type filter used by crawl scoring so the totals
-- agree with score_breakdown.total_scored_pages.
WITH scoreable_pages AS (
    SELECT cp.id
    FROM crawl_pages AS cp
    INNER JOIN crawls AS c ON c.id = cp.crawl_id
    INNER JOIN projects AS p ON p.id = c.project_id
    INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    WHERE cp.crawl_id = $1
      AND om.user_id = $2
      AND (cp.content_type IS NULL OR cp.content_type = '' OR cp.content_type ILIKE '%text/html%')
),
page_issue_counts AS (
    SELECT sp.id, COUNT(ci.id) AS issue_count
    FROM scoreable_pages AS sp
    LEFT JOIN crawl_issues AS ci ON ci.crawl_page_id = sp.id
    GROUP BY sp.id
)
SELECT
    LEAST(issue_count, 20)::int AS issue_count,
    COUNT(*)::bigint AS page_count
FROM page_issue_counts
GROUP BY 1
ORDER BY 1;

-- name: ListCrawlPageSignalsForCrawl :many
SELECT
    url,
    status_code,
    content_type,
    word_count,
    response_time_ms,
    size_bytes
FROM crawl_pages
WHERE crawl_id = $1
ORDER BY created_at ASC;
