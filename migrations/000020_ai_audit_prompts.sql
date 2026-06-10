-- +goose Up
-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_audit_prompts;
-- +goose StatementEnd
