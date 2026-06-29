-- +goose Up
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_audit_runs;
DROP TABLE IF EXISTS ai_audit_prompts;

CREATE TABLE ai_audit_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    audit_id UUID NOT NULL REFERENCES ai_audits(id) ON DELETE CASCADE,
    question_text TEXT NOT NULL,
    display_order INTEGER NOT NULL,
    model_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'success', 'failed')),
    raw_response TEXT,
    parsed_response_json JSONB,
    mentioned_target BOOLEAN,
    target_rank INTEGER,
    visibility_score INTEGER,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (audit_id, display_order, model_name)
);

CREATE INDEX idx_ai_audit_runs_audit_id ON ai_audit_runs(audit_id);
CREATE INDEX idx_ai_audit_runs_status ON ai_audit_runs(status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_audit_runs;

CREATE TABLE ai_audit_prompts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    audit_id UUID NOT NULL REFERENCES ai_audits(id) ON DELETE CASCADE,
    prompt_text TEXT NOT NULL,
    prompt_source TEXT NOT NULL CHECK (prompt_source IN ('expanded')),
    display_order INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ai_audit_prompts_audit_id ON ai_audit_prompts(audit_id);
CREATE UNIQUE INDEX idx_ai_audit_prompts_audit_order ON ai_audit_prompts(audit_id, display_order);

CREATE TABLE ai_audit_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    audit_id UUID NOT NULL REFERENCES ai_audits(id) ON DELETE CASCADE,
    prompt_id UUID NOT NULL REFERENCES ai_audit_prompts(id) ON DELETE CASCADE,
    model_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'success', 'failed')),
    raw_response TEXT,
    parsed_response_json JSONB,
    mentioned_target BOOLEAN,
    target_rank INTEGER,
    visibility_score INTEGER,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (prompt_id, model_name)
);

CREATE INDEX idx_ai_audit_runs_audit_id ON ai_audit_runs(audit_id);
CREATE INDEX idx_ai_audit_runs_prompt_id ON ai_audit_runs(prompt_id);
CREATE INDEX idx_ai_audit_runs_status ON ai_audit_runs(status);
-- +goose StatementEnd
