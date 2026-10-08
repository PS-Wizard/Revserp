-- +goose Up
-- +goose StatementBegin
-- Location business profiles are independent editable copies of the parent
-- profile facts. Later parent edits never propagate; historical runs and the
-- parent profile are untouched by everything below.
CREATE TABLE location_business_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    location_id UUID NOT NULL UNIQUE,
    brand_name TEXT NOT NULL,
    website_url TEXT NOT NULL,
    primary_category TEXT,
    primary_location TEXT,
    business_description TEXT,
    product_description TEXT,
    target_audience TEXT,
    business_competitors JSONB NOT NULL DEFAULT '[]'::jsonb,
    seed_prompts JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(seed_prompts) = 'array'),
    services JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(services) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, project_id)
        REFERENCES project_locations(id, project_id) ON DELETE CASCADE
);
CREATE INDEX location_business_profiles_project_idx ON location_business_profiles(project_id);

-- Copies parent facts once on location insert, including bound locations.
-- product_description stays verbatim. Seed prompts start empty: local AI
-- questions belong to the location keyword layer, not the parent. services
-- snapshots the project service labels at copy time so later parent service
-- edits never propagate to local suggested keywords.
CREATE FUNCTION copy_parent_business_profile_to_location() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO location_business_profiles (
        project_id, location_id, brand_name, website_url, primary_category,
        primary_location, business_description, product_description,
        target_audience, business_competitors, seed_prompts, services
    )
    SELECT NEW.project_id, NEW.id, brand_name, website_url, primary_category,
        primary_location, business_description, product_description,
        target_audience, business_competitors, '[]'::jsonb,
        COALESCE((
            SELECT jsonb_agg(s.label ORDER BY s.normalized_label)
            FROM project_services s WHERE s.project_id = NEW.project_id
        ), '[]'::jsonb)
    FROM project_business_profile
    WHERE project_id = NEW.project_id
    ON CONFLICT (location_id) DO NOTHING;
    RETURN NEW;
END;
$$;
CREATE TRIGGER location_business_profile_copy_on_location_insert
    AFTER INSERT ON project_locations
    FOR EACH ROW EXECUTE FUNCTION copy_parent_business_profile_to_location();

-- Backfill existing locations with one initial copy each. Parent profiles,
-- project services and all historical evidence are only read, never written.
INSERT INTO location_business_profiles (
    project_id, location_id, brand_name, website_url, primary_category,
    primary_location, business_description, product_description,
    target_audience, business_competitors, seed_prompts, services
)
SELECT l.project_id, l.id, pbp.brand_name, pbp.website_url, pbp.primary_category,
    pbp.primary_location, pbp.business_description, pbp.product_description,
    pbp.target_audience, pbp.business_competitors, '[]'::jsonb,
    COALESCE((
        SELECT jsonb_agg(s.label ORDER BY s.normalized_label)
        FROM project_services s WHERE s.project_id = l.project_id
    ), '[]'::jsonb)
FROM project_locations l
JOIN project_business_profile pbp ON pbp.project_id = l.project_id
WHERE NOT EXISTS (
    SELECT 1 FROM location_business_profiles lbp WHERE lbp.location_id = l.id
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS location_business_profile_copy_on_location_insert ON project_locations;
DROP FUNCTION IF EXISTS copy_parent_business_profile_to_location();
DROP TABLE IF EXISTS location_business_profiles;
-- +goose StatementEnd
