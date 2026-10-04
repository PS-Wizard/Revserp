-- +goose NO TRANSACTION
-- +goose Up
-- Concurrent builds can leave invalid indexes after failure. Inspect index
-- validity and migration progress before retrying; do not skip existing names.
CREATE INDEX CONCURRENTLY idx_crawl_links_crawl_id_target_url_key_digest
    ON crawl_links (crawl_id, digest(target_url_key, 'sha256'));
CREATE INDEX CONCURRENTLY idx_crawl_pages_crawl_id_url_key_digest
    ON crawl_pages (crawl_id, digest(url_key, 'sha256'));
DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_crawl_id_target_url_key;
DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_pages_crawl_id_url_key;

-- +goose Down
-- Restoring the raw-key index can fail with SQLSTATE 54000 for long URLs.
-- Create it before dropping the digest indexes so a failed downgrade keeps them.
CREATE INDEX CONCURRENTLY idx_crawl_links_crawl_id_target_url_key
    ON crawl_links (crawl_id, target_url_key);
DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_crawl_id_target_url_key_digest;
DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_pages_crawl_id_url_key_digest;
