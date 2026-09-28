-- +goose NO TRANSACTION
-- +goose Up
-- Replace idx_crawl_links_unique_link with a fixed-size digest equivalent.
--
-- The old UNIQUE index on
-- (crawl_id, source_url, target_url, COALESCE(anchor_text, '')) stores the
-- raw URLs in every index row, so one long link fails production inserts
-- with SQLSTATE 54000 (index row 3152 bytes exceeds the 2704 byte btree
-- limit). The replacement hashes each variable-width column separately to
-- a 32-byte SHA-256 digest, so index entries stay bounded even for long
-- URLs and anchors, while crawl_id stays a leading plain column.
--
-- The same columns participate, NULL anchor_text still equals '' via
-- COALESCE, and the raw columns are untouched, so reports keep reading
-- them. Separate digests preserve column boundaries. SHA-256 collisions are
-- theoretically possible but negligible in practice. digest() (pgcrypto,
-- installed in 000001) is IMMUTABLE and legal in an index expression.
-- CopyCrawlLinksFromBaseline uses ON CONFLICT DO NOTHING without naming
-- an arbiter, so it still works with the replacement unique index.
-- The plain insertCrawlLinkSQL path is unaffected.
--
-- Order matters: the new index is built BEFORE the old one is dropped, so
-- uniqueness is always enforced by at least one index. Both statements use
-- CONCURRENTLY to avoid blocking crawl writes on a multi-million-row
-- table (~3.6M rows / ~711 MB index in production), which forbids running
-- inside a transaction block -- hence NO TRANSACTION above and no
-- StatementBegin/StatementEnd wrapper (goose would send a wrapped block as
-- one multi-statement Exec, which Postgres treats as an implicit
-- transaction and rejects CONCURRENTLY).
--
-- Retry note: CREATE INDEX CONCURRENTLY is not atomic. If it fails,
-- Postgres can leave an INVALID idx_crawl_links_unique_link_digest
-- (indisvalid = false) behind. It is not used for query planning but may
-- still enforce uniqueness. Goose records no version, so a rerun fails
-- loudly on the existing name instead of silently skipping the index.
-- If the old index still exists, recover with:
--   DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_unique_link_digest;
-- then rerun the migration. Keep the old index until the replacement
-- is VALID. If the old index was already dropped, inspect both indexes
-- and migration status before recovery.
CREATE UNIQUE INDEX CONCURRENTLY idx_crawl_links_unique_link_digest
    ON crawl_links (
        crawl_id,
        digest(source_url, 'sha256'),
        digest(target_url, 'sha256'),
        digest(COALESCE(anchor_text, ''), 'sha256')
    );
DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_unique_link;

-- +goose Down
-- Restore the original raw-column unique index first, then drop the
-- digest one, mirroring the Up order so uniqueness is never unenforced.
-- WARNING: rebuilding the old index reintroduces the 2704-byte row limit;
-- on data containing the long links that motivated the Up migration this
-- Down fails with SQLSTATE 54000. That failure leaves the digest index in
-- place, so uniqueness stays enforced.
CREATE UNIQUE INDEX CONCURRENTLY idx_crawl_links_unique_link
    ON crawl_links (
        crawl_id,
        source_url,
        target_url,
        COALESCE(anchor_text, '')
    );
DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_unique_link_digest;
