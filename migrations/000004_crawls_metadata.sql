-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawls
    ADD COLUMN urls_discovered INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN urls_crawled INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN max_depth_reached INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN google_psi_results JSONB,
    ADD COLUMN has_llms_txt BOOLEAN;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawls
    DROP COLUMN IF EXISTS has_llms_txt,
    DROP COLUMN IF EXISTS google_psi_results,
    DROP COLUMN IF EXISTS max_depth_reached,
    DROP COLUMN IF EXISTS urls_crawled,
    DROP COLUMN IF EXISTS urls_discovered;
-- +goose StatementEnd
