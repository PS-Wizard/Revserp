-- name: GetProjectBusinessProfileByProjectID :one
SELECT id, project_id, brand_name, website_url, primary_category, primary_location, business_description, seed_prompts, created_at, updated_at
FROM project_business_profile
WHERE project_id = $1
LIMIT 1;

-- name: UpsertProjectBusinessProfile :one
INSERT INTO project_business_profile (
    project_id,
    brand_name,
    website_url,
    primary_category,
    primary_location,
    business_description,
    seed_prompts
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7
)
ON CONFLICT (project_id) DO UPDATE SET
    brand_name = excluded.brand_name,
    website_url = excluded.website_url,
    primary_category = excluded.primary_category,
    primary_location = excluded.primary_location,
    business_description = excluded.business_description,
    seed_prompts = excluded.seed_prompts,
    updated_at = now()
RETURNING id, project_id, brand_name, website_url, primary_category, primary_location, business_description, seed_prompts, created_at, updated_at;
