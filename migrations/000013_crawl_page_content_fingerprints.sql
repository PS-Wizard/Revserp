-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages
	ADD COLUMN content_sha256 TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_pages
	DROP COLUMN IF EXISTS content_sha256;
-- +goose StatementEnd
