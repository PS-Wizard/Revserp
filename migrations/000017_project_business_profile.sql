-- +goose Up
-- +goose StatementBegin
CREATE TABLE project_business_profile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    brand_name TEXT NOT NULL,
    website_url TEXT NOT NULL,
    primary_category TEXT,
    primary_location TEXT,
    business_description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_business_profile_project_id ON project_business_profile(project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS project_business_profile;
-- +goose StatementEnd
