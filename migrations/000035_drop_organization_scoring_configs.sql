-- +goose Up
-- +goose StatementBegin
DROP TABLE IF EXISTS organization_scoring_configs;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE organization_scoring_configs (
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    config_json JSONB NOT NULL,
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT organization_scoring_configs_org_id_unique UNIQUE (org_id)
);
-- +goose StatementEnd
