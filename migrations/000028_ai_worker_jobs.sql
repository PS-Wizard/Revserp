-- +goose Up
-- +goose StatementBegin
CREATE TABLE ai_worker_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_type TEXT NOT NULL CHECK (job_type IN ('prompt_generation', 'visibility_run')),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ai_worker_jobs_status ON ai_worker_jobs(status, created_at);
CREATE INDEX idx_ai_worker_jobs_project_id ON ai_worker_jobs(project_id);

CREATE TABLE project_ai_questions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    questions JSONB NOT NULL DEFAULT '[]',
    generation_model TEXT NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS project_ai_questions;
DROP TABLE IF EXISTS ai_worker_jobs;
-- +goose StatementEnd
