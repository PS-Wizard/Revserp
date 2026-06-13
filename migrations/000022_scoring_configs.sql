-- +goose Up
-- +goose StatementBegin
CREATE TABLE scoring_configs (
    id BIGINT PRIMARY KEY,
    config_json JSONB NOT NULL,
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS scoring_configs;
-- +goose StatementEnd
