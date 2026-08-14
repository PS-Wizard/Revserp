-- +goose Up
ALTER TABLE organization_features
ADD COLUMN ai_use_internal_prompt BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE organization_features
DROP COLUMN IF EXISTS ai_use_internal_prompt;
