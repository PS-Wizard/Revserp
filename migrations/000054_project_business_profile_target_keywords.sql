-- +goose Up
-- +goose StatementBegin
ALTER TABLE project_business_profile
ADD COLUMN IF NOT EXISTS target_keywords JSONB NOT NULL DEFAULT '[]'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE project_business_profile
DROP COLUMN IF EXISTS target_keywords;
-- +goose StatementEnd
