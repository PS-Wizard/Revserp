-- name: GetProjectBusinessProfileByProjectID :one
SELECT id, project_id, brand_name, website_url, primary_category, primary_location, business_description, seed_prompts, target_keywords, created_at, updated_at
FROM project_business_profile
WHERE project_id = $1
LIMIT 1;

-- name: GetProjectByIDForUserForBusinessProfileUpdate :one
SELECT p.id, p.organization_id, p.name, p.base_url, p.created_at
FROM projects AS p
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE p.id = $1
  AND om.user_id = $2
LIMIT 1
FOR UPDATE;

-- name: UpsertProjectBusinessProfile :one
INSERT INTO project_business_profile (
    project_id,
    brand_name,
    website_url,
    primary_category,
    primary_location,
    business_description,
    seed_prompts,
    target_keywords
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8
)
ON CONFLICT (project_id) DO UPDATE SET
    brand_name = excluded.brand_name,
    website_url = excluded.website_url,
    primary_category = excluded.primary_category,
    primary_location = excluded.primary_location,
    business_description = excluded.business_description,
    seed_prompts = excluded.seed_prompts,
    target_keywords = excluded.target_keywords,
    updated_at = now()
RETURNING id, project_id, brand_name, website_url, primary_category, primary_location, business_description, seed_prompts, target_keywords, created_at, updated_at;

-- name: GetProjectBusinessProfileByProjectIDForUser :one
SELECT
    pbp.id,
    pbp.project_id,
    pbp.brand_name,
    pbp.website_url,
    pbp.primary_category,
    pbp.primary_location,
    pbp.business_description,
    pbp.seed_prompts,
    pbp.target_keywords,
    pbp.created_at,
    pbp.updated_at
FROM project_business_profile AS pbp
INNER JOIN projects AS p ON p.id = pbp.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE pbp.project_id = $1
  AND om.user_id = $2
LIMIT 1;
