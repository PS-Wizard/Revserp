-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
  ADD COLUMN is_platform_admin BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN status TEXT NOT NULL DEFAULT 'active',
  ADD COLUMN suspended_at TIMESTAMPTZ,
  ADD COLUMN suspension_reason TEXT;

CREATE TABLE organization_scoring_configs (
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    config_json JSONB NOT NULL,
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT organization_scoring_configs_org_id_unique UNIQUE (org_id)
);

CREATE TABLE ai_prompt_configs (
    id BIGINT PRIMARY KEY,
    context_prompt TEXT NOT NULL DEFAULT '',
    guidelines_prompt TEXT NOT NULL DEFAULT '',
    other_notes_prompt TEXT NOT NULL DEFAULT '',
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_prompt_configs;
DROP TABLE IF EXISTS organization_scoring_configs;
ALTER TABLE users
  DROP COLUMN IF EXISTS suspension_reason,
  DROP COLUMN IF EXISTS suspended_at,
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS is_platform_admin;
-- +goose StatementEnd
