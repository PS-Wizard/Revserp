-- +goose Up
-- +goose StatementBegin
CREATE TABLE crawls (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    config_snapshot JSONB,
    seo_score INTEGER,
    aeo_score INTEGER,
    pagespeed_score INTEGER,
    overall_score INTEGER,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crawls_project_id ON crawls(project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS crawls;
-- +goose StatementEnd
