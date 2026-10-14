-- +goose Up
-- +goose StatementBegin
ALTER TABLE project_auto_crawl_settings
    DROP CONSTRAINT IF EXISTS project_auto_crawl_settings_frequency_days_check;
ALTER TABLE project_auto_crawl_settings
    ADD CONSTRAINT project_auto_crawl_settings_frequency_days_check
    CHECK (frequency_days BETWEEN 1 AND 60);
ALTER TABLE project_auto_crawl_settings
    ALTER COLUMN frequency_days SET DEFAULT 7;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE project_auto_crawl_settings
    DROP CONSTRAINT IF EXISTS project_auto_crawl_settings_frequency_days_check;
ALTER TABLE project_auto_crawl_settings
    ADD CONSTRAINT project_auto_crawl_settings_frequency_days_check
    CHECK (frequency_days BETWEEN 1 AND 30);
ALTER TABLE project_auto_crawl_settings
    ALTER COLUMN frequency_days SET DEFAULT 1;
-- +goose StatementEnd
