-- +goose Up
-- +goose StatementBegin
-- Non-2xx pages are now persisted instead of skipped, so a page row needs to
-- carry why it is unhealthy. status_code already covers hard HTTP errors;
-- these two cover the cases HTTP status cannot express.
ALTER TABLE crawl_pages
    ADD COLUMN soft_404 BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN fetch_error TEXT;

-- The site graph and broken-link derivation both filter on unhealthy pages.
CREATE INDEX idx_crawl_pages_crawl_id_status_code ON crawl_pages(crawl_id, status_code);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_crawl_pages_crawl_id_status_code;

ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS fetch_error,
    DROP COLUMN IF EXISTS soft_404;
-- +goose StatementEnd
