-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages
    ADD COLUMN content_blocks JSONB;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS content_blocks;
-- +goose StatementEnd
