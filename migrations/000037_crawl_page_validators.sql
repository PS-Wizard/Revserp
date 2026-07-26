-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages
    ADD COLUMN etag TEXT,
    ADD COLUMN last_modified TEXT;

CREATE INDEX idx_crawls_project_id_completed_at ON crawls(project_id, completed_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_crawls_project_id_completed_at;

ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS last_modified,
    DROP COLUMN IF EXISTS etag;
-- +goose StatementEnd
