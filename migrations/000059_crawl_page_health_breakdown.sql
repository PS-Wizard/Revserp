-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_pages ADD COLUMN health_breakdown JSONB;
ALTER TABLE crawl_pages ADD CONSTRAINT chk_crawl_pages_health_breakdown_object CHECK (health_breakdown IS NULL OR jsonb_typeof(health_breakdown) = 'object');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_pages DROP CONSTRAINT IF EXISTS chk_crawl_pages_health_breakdown_object;
ALTER TABLE crawl_pages DROP COLUMN IF EXISTS health_breakdown;
-- +goose StatementEnd
