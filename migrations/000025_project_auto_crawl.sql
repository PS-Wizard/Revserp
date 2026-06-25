-- +goose Up
-- +goose StatementBegin

-- Add source column to crawls
ALTER TABLE crawls
    ADD COLUMN source text NOT NULL DEFAULT 'manual';

ALTER TABLE crawls
    ADD CONSTRAINT crawls_source_check CHECK (source IN ('manual', 'auto'));

-- Auto-crawl settings per project
CREATE TABLE project_auto_crawl_settings (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    config_snapshot jsonb,
    last_enqueued_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Indexes for scheduler sweep and worker claim
CREATE INDEX idx_project_auto_crawl_settings_enabled_last_enqueued
    ON project_auto_crawl_settings(enabled, last_enqueued_at)
    WHERE enabled = true;

CREATE INDEX idx_crawls_source_status_project ON crawls(source, status, project_id);

CREATE INDEX idx_crawls_project_status ON crawls(project_id, status)
    WHERE source = 'auto';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_crawls_project_status;
DROP INDEX IF EXISTS idx_crawls_source_status_project;
DROP INDEX IF EXISTS idx_project_auto_crawl_settings_enabled_last_enqueued;

DROP TABLE IF EXISTS project_auto_crawl_settings;

ALTER TABLE crawls DROP CONSTRAINT IF EXISTS crawls_source_check;
ALTER TABLE crawls DROP COLUMN IF EXISTS source;

-- +goose StatementEnd
