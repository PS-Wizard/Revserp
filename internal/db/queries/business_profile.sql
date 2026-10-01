-- name: GetProjectBusinessProfileByProjectID :one
SELECT pbp.id, pbp.project_id, pbp.brand_name, pbp.website_url, pbp.primary_category, pbp.primary_location, pbp.business_description, pbp.product_description, pbp.target_audience, pbp.business_competitors,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = pbp.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won WHERE won.kind = 'brand') AS branded_keywords,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = pbp.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won WHERE won.kind = 'non_brand') AS non_branded_keywords,
    pbp.seed_prompts,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = pbp.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won) AS target_keywords,
    pbp.created_at, pbp.updated_at
FROM project_business_profile AS pbp
WHERE pbp.project_id = $1
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
WITH upserted AS (
INSERT INTO project_business_profile (
    project_id,
    brand_name,
    website_url,
    primary_category,
    primary_location,
    business_description,
    product_description,
    target_audience,
    business_competitors,
    seed_prompts
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9,
    $10
)
ON CONFLICT (project_id) DO UPDATE SET
    brand_name = excluded.brand_name,
    website_url = excluded.website_url,
    primary_category = excluded.primary_category,
    primary_location = excluded.primary_location,
    business_description = excluded.business_description,
    product_description = excluded.product_description,
    target_audience = excluded.target_audience,
    business_competitors = excluded.business_competitors,
    seed_prompts = excluded.seed_prompts,
    updated_at = now()
RETURNING id, project_id, brand_name, website_url, primary_category, primary_location, business_description, product_description, target_audience, business_competitors, seed_prompts, created_at, updated_at
)
SELECT u.id, u.project_id, u.brand_name, u.website_url, u.primary_category, u.primary_location, u.business_description, u.product_description, u.target_audience, u.business_competitors,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = u.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won WHERE won.kind = 'brand') AS branded_keywords,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = u.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won WHERE won.kind = 'non_brand') AS non_branded_keywords,
    u.seed_prompts,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = u.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won) AS target_keywords,
    u.created_at, u.updated_at
FROM upserted AS u;

-- name: GetProjectBusinessProfileByProjectIDForUser :one
SELECT
    pbp.id,
    pbp.project_id,
    pbp.brand_name,
    pbp.website_url,
    pbp.primary_category,
    pbp.primary_location,
    pbp.business_description,
    pbp.product_description,
    pbp.target_audience,
    pbp.business_competitors,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = pbp.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won WHERE won.kind = 'brand') AS branded_keywords,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = pbp.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won WHERE won.kind = 'non_brand') AS non_branded_keywords,
    pbp.seed_prompts,
    (SELECT jsonb_agg(won.keyword ORDER BY won.normalized_keyword) FROM (SELECT DISTINCT ON (pk.normalized_keyword) pk.keyword, pk.normalized_keyword, pk.kind FROM project_keywords AS pk WHERE pk.project_id = pbp.project_id ORDER BY pk.normalized_keyword, CASE WHEN pk.source = 'user' THEN 0 ELSE 1 END) AS won) AS target_keywords,
    pbp.created_at,
    pbp.updated_at
FROM project_business_profile AS pbp
INNER JOIN projects AS p ON p.id = pbp.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE pbp.project_id = $1
  AND om.user_id = $2
LIMIT 1;
