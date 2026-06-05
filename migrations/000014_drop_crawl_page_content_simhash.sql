-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS content_simhash;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_pages
    ADD COLUMN IF NOT EXISTS content_simhash BIGINT;
-- +goose StatementEnd
