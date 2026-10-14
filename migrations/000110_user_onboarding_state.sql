-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN onboarding_state JSONB NOT NULL DEFAULT '{"version":1,"status":"not_started","checkpoint":"new_project"}';
UPDATE users
SET onboarding_state = '{"version":1,"status":"skipped","checkpoint":"new_project"}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN onboarding_state;
-- +goose StatementEnd
