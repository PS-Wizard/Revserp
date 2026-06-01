-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages
    ADD COLUMN visible_text TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_pages
    DROP COLUMN IF EXISTS visible_text;
-- +goose StatementEnd
