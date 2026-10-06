-- name: CreateLocationSetupForUser :one
INSERT INTO project_locations(project_id,name,latitude,longitude,address,locality,localities)
SELECT p.id,sqlc.arg(name)::text,sqlc.arg(latitude)::double precision,sqlc.arg(longitude)::double precision,
sqlc.arg(address)::text,sqlc.arg(locality)::text,sqlc.arg(localities)::jsonb
FROM projects p JOIN organization_members m ON m.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id)::uuid AND m.user_id = sqlc.arg(user_id)::uuid
RETURNING *;

-- name: ListProjectLocationsForUser :many
SELECT l.* FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE p.id = $1 AND m.user_id = $2
ORDER BY l.created_at, l.id;


-- name: GetLocationForListingLookup :one
SELECT l.*, p.organization_id FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = $1 AND p.id = $2 AND m.user_id = $3
FOR NO KEY UPDATE OF l;

-- name: CreateLocationListingLookup :one
INSERT INTO local_listing_lookups(location_id,query,expected_credits,reserved_credits,candidate_key,source_latitude,source_longitude,created_at)
VALUES(sqlc.arg(location_id)::uuid,sqlc.arg(query)::text,sqlc.arg(expected_credits)::integer,
sqlc.arg(expected_credits)::integer,sqlc.arg(candidate_key)::text,
sqlc.arg(source_latitude)::double precision,sqlc.arg(source_longitude)::double precision,clock_timestamp()) RETURNING *;

-- name: CreatePlacesListingLookup :one
INSERT INTO local_listing_lookups(location_id,query,expected_credits,reserved_credits,credit_known,candidate_key,source_latitude,source_longitude,created_at)
VALUES(sqlc.arg(location_id)::uuid,sqlc.arg(query)::text,0,0,TRUE,sqlc.arg(candidate_key)::text,
sqlc.arg(source_latitude)::double precision,sqlc.arg(source_longitude)::double precision,clock_timestamp()) RETURNING *;

-- name: GetLocationListingLookup :one
SELECT s.*,p.organization_id FROM local_listing_lookups s
JOIN project_locations l ON l.id = s.location_id
JOIN projects p ON p.id = l.project_id
WHERE s.id = $1;

-- name: GetListingLookupForUser :one
SELECT s.* FROM local_listing_lookups s
JOIN project_locations l ON l.id = s.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE s.id = $1 AND l.id = $2 AND p.id = $3 AND m.user_id = $4;

-- name: GetLatestListingLookupForUser :one
SELECT s.* FROM local_listing_lookups s
JOIN project_locations l ON l.id = s.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = $1 AND p.id = $2 AND m.user_id = $3
ORDER BY s.created_at DESC,s.id DESC LIMIT 1;

-- name: LockLocationListingLookup :one
SELECT * FROM local_listing_lookups WHERE id = $1 FOR UPDATE;

-- name: CompleteLocationListingLookup :one
UPDATE local_listing_lookups SET status = $2, reserved_credits = $3, credits_used = $4,
credit_known = $5, raw_response = $6, error = $7, completed_at = now()
WHERE id = $1 AND status = 'running' RETURNING *;

-- name: BindLocationListingForUser :one
UPDATE project_locations l SET place_id = sqlc.arg(place_id)::text,
latitude = sqlc.arg(latitude)::double precision, longitude = sqlc.arg(longitude)::double precision,
locality = sqlc.arg(locality)::text, localities = COALESCE(sqlc.arg(localities)::jsonb, '[]'::jsonb), updated_at = now()
FROM projects p,organization_members m
WHERE l.id = $1 AND l.project_id = p.id AND p.id = $2
AND m.org_id = p.organization_id AND m.user_id = $3 AND l.place_id IS NULL
RETURNING l.*;

-- name: UnbindLocationListingForUser :one
UPDATE project_locations l SET place_id = NULL, updated_at = now()
FROM projects p,organization_members m
WHERE l.id = $1 AND l.project_id = p.id AND p.id = $2
AND m.org_id = p.organization_id AND m.user_id = $3
RETURNING l.*;

-- name: GetLocationGeographyCache :one
SELECT * FROM location_geography_cache WHERE cache_key = $1;

-- name: SaveLocationGeographyCache :exec
INSERT INTO location_geography_cache(cache_key,results) VALUES($1,$2)
ON CONFLICT(cache_key) DO UPDATE SET results = EXCLUDED.results,updated_at = now();

-- name: DeleteLocationSetupForUser :execrows
DELETE FROM project_locations l USING projects p,organization_members m
WHERE l.id = $1 AND l.project_id = p.id AND p.id = $2
AND m.org_id = p.organization_id AND m.user_id = $3;
