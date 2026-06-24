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
    json_ld
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
    $33
)
RETURNING id, crawl_id, url, status_code, content_type, size_bytes, is_internal, depth, title, meta_description, h1, h1_count, h2_count, h3_count, word_count, visible_text, content_sha256, author, canonical_url, lang, viewport, robots, image_count, images_without_alt_count, images_without_dimensions, external_links, internal_links, response_time_ms, javascript_rendered, h2_headings, h3_headings, heading_outline, og_tags, json_ld, created_at;

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
    cp.h2_headings,
    cp.h3_headings,
    cp.heading_outline,
    cp.og_tags,
    cp.json_ld,
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
    h2_headings,
    h3_headings,
    heading_outline,
    og_tags,
    json_ld,
    created_at
FROM crawl_pages
WHERE crawl_id = $1
ORDER BY created_at ASC
LIMIT $2;

-- name: UpdateCrawlPageContentFingerprints :exec
UPDATE crawl_pages
SET content_sha256 = $2
WHERE id = $1;
