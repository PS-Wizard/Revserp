-- +goose Up
-- Both tables store raw URLs, so one page appears as several strings
-- (".../a", ".../a/", ".../A#top"). Resolving a link's target status used to
-- normalize both sides inside the join, which no index can serve: it forced a
-- merge join over 114k rows with a disk-spilling sort, and a 90s statement
-- timeout canceled it mid-crawl. Storing the normalized form once turns the
-- join into a plain indexed equality, and the key cannot drift from the URL
-- because Postgres computes it.
ALTER TABLE crawl_pages
    ADD COLUMN url_key TEXT GENERATED ALWAYS AS (
        regexp_replace(lower(split_part(url, '#', 1)), '(.)/+$', '\1')
    ) STORED;

ALTER TABLE crawl_links
    ADD COLUMN target_url_key TEXT GENERATED ALWAYS AS (
        regexp_replace(lower(split_part(target_url, '#', 1)), '(.)/+$', '\1')
    ) STORED;

CREATE INDEX idx_crawl_pages_crawl_id_url_key ON crawl_pages (crawl_id, url_key);
CREATE INDEX idx_crawl_links_crawl_id_target_url_key ON crawl_links (crawl_id, target_url_key);

-- +goose Down
DROP INDEX IF EXISTS idx_crawl_links_crawl_id_target_url_key;
DROP INDEX IF EXISTS idx_crawl_pages_crawl_id_url_key;
ALTER TABLE crawl_links DROP COLUMN IF EXISTS target_url_key;
ALTER TABLE crawl_pages DROP COLUMN IF EXISTS url_key;
