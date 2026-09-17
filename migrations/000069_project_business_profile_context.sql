-- +goose Up
-- +goose StatementBegin
ALTER TABLE project_business_profile
ADD COLUMN IF NOT EXISTS product_description TEXT,
ADD COLUMN IF NOT EXISTS target_audience TEXT,
ADD COLUMN IF NOT EXISTS business_competitors JSONB NOT NULL DEFAULT '[]'::jsonb,
ADD COLUMN IF NOT EXISTS branded_keywords JSONB NOT NULL DEFAULT '[]'::jsonb,
ADD COLUMN IF NOT EXISTS non_branded_keywords JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE project_business_profile
SET non_branded_keywords = target_keywords
WHERE target_keywords IS NOT NULL AND target_keywords <> '[]'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE project_business_profile
DROP COLUMN IF EXISTS product_description,
DROP COLUMN IF EXISTS target_audience,
DROP COLUMN IF EXISTS business_competitors,
DROP COLUMN IF EXISTS branded_keywords,
DROP COLUMN IF EXISTS non_branded_keywords;
-- +goose StatementEnd
