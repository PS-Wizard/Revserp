-- +goose Up
-- +goose StatementBegin
ALTER TABLE project_business_profile
ADD COLUMN seed_prompts JSONB NOT NULL DEFAULT '[]'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE project_business_profile
DROP COLUMN IF EXISTS seed_prompts;
-- +goose StatementEnd
