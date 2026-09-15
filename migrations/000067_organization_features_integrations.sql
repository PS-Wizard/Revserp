-- +goose Up
-- +goose StatementBegin
ALTER TABLE organization_features
    ADD COLUMN integrations BOOLEAN NOT NULL DEFAULT TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE organization_features
    DROP COLUMN IF EXISTS integrations;
-- +goose StatementEnd
