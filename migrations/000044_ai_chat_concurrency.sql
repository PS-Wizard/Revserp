-- +goose Up
ALTER TABLE organization_features
    ADD COLUMN ai_concurrent_turn_limit_per_user INTEGER NOT NULL DEFAULT 2
    CHECK (ai_concurrent_turn_limit_per_user BETWEEN 1 AND 20);

-- +goose Down
ALTER TABLE organization_features
    DROP COLUMN IF EXISTS ai_concurrent_turn_limit_per_user;
