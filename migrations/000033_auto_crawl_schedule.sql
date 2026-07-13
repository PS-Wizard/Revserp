-- +goose Up
-- +goose StatementBegin

-- Per-project auto-crawl schedule: every N days at a wall-clock time in the
-- project's timezone. next_run_at is precomputed on every settings write and
-- advanced by the scheduler after each enqueue, so the sweep is a single
-- indexed comparison against now().
ALTER TABLE project_auto_crawl_settings
    ADD COLUMN frequency_days int NOT NULL DEFAULT 1,
    ADD COLUMN run_at time NOT NULL DEFAULT '03:00',
    ADD COLUMN timezone text NOT NULL DEFAULT 'UTC',
    ADD COLUMN next_run_at timestamptz;

ALTER TABLE project_auto_crawl_settings
    ADD CONSTRAINT project_auto_crawl_settings_frequency_days_check
    CHECK (frequency_days BETWEEN 1 AND 30);

-- Existing enabled rows become due immediately; the scheduler advances them
-- onto their proper slot after the first enqueue.
UPDATE project_auto_crawl_settings SET next_run_at = now() WHERE enabled = true;

CREATE INDEX idx_project_auto_crawl_settings_next_run
    ON project_auto_crawl_settings(next_run_at)
    WHERE enabled = true;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_project_auto_crawl_settings_next_run;

ALTER TABLE project_auto_crawl_settings
    DROP CONSTRAINT IF EXISTS project_auto_crawl_settings_frequency_days_check;

ALTER TABLE project_auto_crawl_settings
    DROP COLUMN IF EXISTS next_run_at,
    DROP COLUMN IF EXISTS timezone,
    DROP COLUMN IF EXISTS run_at,
    DROP COLUMN IF EXISTS frequency_days;

-- +goose StatementEnd
