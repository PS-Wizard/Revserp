-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages ADD COLUMN health_score SMALLINT;
ALTER TABLE crawl_pages ADD CONSTRAINT chk_crawl_pages_health_score_range CHECK (health_score IS NULL OR (health_score >= 0 AND health_score <= 100));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_pages DROP CONSTRAINT IF EXISTS chk_crawl_pages_health_score_range;
ALTER TABLE crawl_pages DROP COLUMN IF EXISTS health_score;
-- +goose StatementEnd
