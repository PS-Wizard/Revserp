-- +goose Up
-- +goose StatementBegin
ALTER TABLE organization_features
    ADD COLUMN max_projects INTEGER NOT NULL DEFAULT 5;

ALTER TABLE organization_features
    ADD CONSTRAINT chk_organization_features_max_projects CHECK (max_projects >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE organization_features DROP CONSTRAINT IF EXISTS chk_organization_features_max_projects;
ALTER TABLE organization_features DROP COLUMN IF EXISTS max_projects;
-- +goose StatementEnd
