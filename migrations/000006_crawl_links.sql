-- +goose Up
-- +goose StatementBegin
CREATE TABLE crawl_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    source_url TEXT NOT NULL,
    target_url TEXT NOT NULL,
    anchor_text TEXT,
    is_internal BOOLEAN,
    target_status INTEGER,
    nofollow BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crawl_links_crawl_id ON crawl_links(crawl_id);
CREATE UNIQUE INDEX idx_crawl_links_unique_link ON crawl_links(
    crawl_id,
    source_url,
    target_url,
    COALESCE(anchor_text, '')
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS crawl_links;
-- +goose StatementEnd
