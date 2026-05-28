-- +goose Up
-- +goose StatementBegin
CREATE TABLE crawl_pages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    status_code INTEGER,
    content_type TEXT,
    size_bytes INTEGER,
    is_internal BOOLEAN,
    depth INTEGER,
    title TEXT,
    meta_description TEXT,
    h1 TEXT,
    h1_count INTEGER,
    h2_count INTEGER,
    h3_count INTEGER,
    word_count INTEGER,
    author TEXT,
    canonical_url TEXT,
    lang TEXT,
    viewport TEXT,
    robots TEXT,
    image_count INTEGER,
    images_without_alt_count INTEGER,
    images_without_dimensions INTEGER,
    external_links INTEGER,
    internal_links INTEGER,
    response_time_ms INTEGER,
    javascript_rendered BOOLEAN,
    h2_headings JSONB,
    h3_headings JSONB,
    heading_outline JSONB,
    og_tags JSONB,
    json_ld JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (crawl_id, url)
);

CREATE INDEX idx_crawl_pages_crawl_id ON crawl_pages(crawl_id);
CREATE INDEX idx_crawl_pages_crawl_id_url ON crawl_pages(crawl_id, url);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS crawl_pages;
-- +goose StatementEnd
