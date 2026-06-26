-- +goose Up
-- +goose StatementBegin

CREATE INDEX IF NOT EXISTS idx_organization_members_user_id ON organization_members(user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_organization_members_user_id;

-- +goose StatementEnd
