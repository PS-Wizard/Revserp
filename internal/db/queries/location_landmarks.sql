-- name: ListLocationLandmarksForUser :many
SELECT ll.* FROM location_landmarks ll
JOIN project_locations l ON l.id = ll.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE ll.location_id = sqlc.arg(location_id)::uuid
  AND l.project_id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
ORDER BY ll.name, ll.id;

-- selected is absent from the SET list, so a user's tick survives a refresh.
-- name: UpsertLocationLandmarkForUser :one
INSERT INTO location_landmarks(location_id, name, latitude, longitude, straight_line_m, provider, provider_ref, categories, fetched_at)
SELECT l.id, sqlc.arg(name)::text, sqlc.arg(latitude)::double precision,
       sqlc.arg(longitude)::double precision, sqlc.arg(straight_line_m)::integer,
       sqlc.arg(provider)::text,
       sqlc.arg(provider_ref)::text, sqlc.arg(categories)::text[],
       sqlc.arg(fetched_at)::timestamptz
FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
ON CONFLICT (location_id, provider_ref) DO UPDATE SET
    name = EXCLUDED.name,
    latitude = EXCLUDED.latitude,
    longitude = EXCLUDED.longitude,
    straight_line_m = EXCLUDED.straight_line_m,
    provider = EXCLUDED.provider,
    categories = EXCLUDED.categories,
    fetched_at = EXCLUDED.fetched_at
RETURNING *;

-- Caller runs this only after a successful fetch, passing the full set of provider_refs to retain.
-- name: DeleteRemovedLocationLandmarksForUser :execrows
DELETE FROM location_landmarks ll
USING project_locations l, projects p, organization_members m
WHERE ll.location_id = l.id AND l.id = sqlc.arg(location_id)::uuid
  AND l.project_id = p.id AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid
  AND NOT (ll.provider_ref = ANY(sqlc.arg(keep_provider_refs)::text[]));

-- name: UpdateLocationLandmarkSelectionForUser :one
UPDATE location_landmarks ll SET selected = sqlc.arg(selected)::boolean
FROM project_locations l, projects p, organization_members m
WHERE ll.id = sqlc.arg(id)::uuid AND ll.location_id = l.id AND l.project_id = p.id
  AND ll.location_id = sqlc.arg(location_id)::uuid
  AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid
RETURNING ll.*;

-- name: LockProjectLocationForLandmarkRefreshForUser :one
SELECT l.id, l.project_id, l.place_id, l.latitude, l.longitude, l.name
FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
FOR NO KEY UPDATE OF l;
