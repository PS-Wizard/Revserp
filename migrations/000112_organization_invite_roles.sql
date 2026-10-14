-- +goose Up
-- +goose StatementBegin
ALTER TABLE organization_invites
  ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
ALTER TABLE organization_invites
  ADD CONSTRAINT organization_invites_role_check CHECK (role IN ('member', 'manager'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE organization_invites DROP CONSTRAINT IF EXISTS organization_invites_role_check;
ALTER TABLE organization_invites DROP COLUMN IF EXISTS role;
-- +goose StatementEnd
