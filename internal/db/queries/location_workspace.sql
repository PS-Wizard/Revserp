-- name: GetLocationBusinessProfile :one
SELECT id, project_id, location_id, brand_name, website_url,
    primary_category, primary_location, business_description, product_description,
    target_audience, business_competitors, seed_prompts, services, created_at, updated_at
FROM location_business_profiles WHERE project_id = $1 AND location_id = $2 LIMIT 1;

-- name: UpsertLocationBusinessProfile :one
INSERT INTO location_business_profiles
    (project_id, location_id, brand_name, website_url, primary_category, primary_location,
    business_description, product_description, target_audience, business_competitors, seed_prompts, services)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($12, '[]'::jsonb))
    ON CONFLICT (location_id) DO UPDATE SET
    brand_name = excluded.brand_name, website_url = excluded.website_url,
    primary_category = excluded.primary_category, primary_location = excluded.primary_location,
    business_description = excluded.business_description,
    product_description = excluded.product_description,
    target_audience = excluded.target_audience,
    business_competitors = excluded.business_competitors,
    seed_prompts = excluded.seed_prompts,
    services = CASE WHEN $12 IS NULL THEN location_business_profiles.services ELSE excluded.services END,
    updated_at = now()
    RETURNING id, project_id, location_id, brand_name, website_url,
    primary_category, primary_location, business_description, product_description,
    target_audience, business_competitors, seed_prompts, services, created_at, updated_at;

-- name: ListLocationWebsiteScopeRevisions :many
SELECT id, revision, url, match, created_at
    FROM location_website_scopes WHERE location_id = $1 ORDER BY revision DESC;

-- name: GetLatestLocationWebsiteScopeRevision :one
SELECT id, revision, url, match, created_at
    FROM location_website_scopes WHERE location_id = $1 ORDER BY revision DESC LIMIT 1;

-- name: InsertLocationWebsiteScopeRevision :one
INSERT INTO location_website_scopes
    (project_id, location_id, revision, url, match)
    SELECT $1, $2, COALESCE((SELECT MAX(revision) FROM location_website_scopes WHERE location_id = $2), 0) + 1, $3, $4
    RETURNING id, revision, created_at;
