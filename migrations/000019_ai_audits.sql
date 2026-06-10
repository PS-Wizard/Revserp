-- +goose Up
-- +goose StatementBegin
CREATE TABLE ai_audits (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'completed_with_failures', 'failed')),
    score INTEGER,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ai_audits_project_id ON ai_audits(project_id);
CREATE INDEX idx_ai_audits_crawl_id ON ai_audits(crawl_id);
CREATE INDEX idx_ai_audits_status ON ai_audits(status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_audits;
-- +goose StatementEnd
