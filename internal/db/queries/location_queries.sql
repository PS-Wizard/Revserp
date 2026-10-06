-- Draft rows for a location. A run reads its own frozen snapshot, never this table.

-- name: ListProjectLocationQueriesForUser :many
SELECT q.* FROM project_location_queries q
JOIN project_locations l ON l.id = q.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE q.location_id = sqlc.arg(location_id)::uuid
  AND l.project_id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
ORDER BY q.kind, q.ordinal, q.id;

-- name: LockProjectLocationQueriesForUser :many
SELECT q.* FROM project_location_queries q
JOIN project_locations l ON l.id = q.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE q.location_id = sqlc.arg(location_id)::uuid
  AND l.project_id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
ORDER BY q.kind, q.ordinal, q.id
FOR UPDATE OF q;

-- name: LockProjectLocationForQueryDraftForUser :one
-- Locks the parent location so concurrent draft writes for it serialize, including inserts into an empty draft.
SELECT l.id, l.project_id, l.place_id, l.latitude, l.longitude, l.name, p.organization_id
FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
FOR NO KEY UPDATE OF l;

-- name: ListEnabledMapQueriesForUser :many
SELECT q.* FROM project_location_queries q
JOIN project_locations l ON l.id = q.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE q.location_id = sqlc.arg(location_id)::uuid
  AND l.project_id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
  AND q.kind = 'map' AND q.enabled = TRUE
ORDER BY q.ordinal, q.id;

-- name: InsertProjectLocationQueryForUser :one
INSERT INTO project_location_queries(location_id, text, normalized, ordinal, enabled, kind, source, origin, landmark_id)
SELECT l.id, sqlc.arg(text)::text, sqlc.arg(normalized)::text, sqlc.arg(ordinal)::integer,
       sqlc.arg(enabled)::boolean, sqlc.arg(kind)::text, sqlc.arg(source)::text,
       sqlc.arg(origin)::text, sqlc.arg(landmark_id)::uuid
FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
  AND (sqlc.arg(landmark_id)::uuid IS NULL
       OR EXISTS (SELECT 1 FROM location_landmarks ll
                  WHERE ll.id = sqlc.arg(landmark_id)::uuid AND ll.location_id = l.id))
RETURNING *;

-- name: UpdateProjectLocationQueryForUser :one
UPDATE project_location_queries q SET text = sqlc.arg(text)::text,
    normalized = sqlc.arg(normalized)::text, ordinal = sqlc.arg(ordinal)::integer,
    enabled = sqlc.arg(enabled)::boolean, kind = sqlc.arg(kind)::text,
    source = sqlc.arg(source)::text, origin = sqlc.arg(origin)::text,
    landmark_id = sqlc.arg(landmark_id)::uuid, updated_at = now()
FROM project_locations l, projects p, organization_members m
WHERE q.id = sqlc.arg(id)::uuid AND q.location_id = l.id AND l.project_id = p.id
  AND q.location_id = sqlc.arg(location_id)::uuid
  AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid
  AND (sqlc.arg(landmark_id)::uuid IS NULL
       OR EXISTS (SELECT 1 FROM location_landmarks ll
                  WHERE ll.id = sqlc.arg(landmark_id)::uuid AND ll.location_id = l.id))
RETURNING q.*;

-- Manual rows only: an omission that targets a generated query disables it instead of deleting.
-- name: DeleteProjectLocationQueryForUser :execrows
DELETE FROM project_location_queries q
USING project_locations l, projects p, organization_members m
WHERE q.id = sqlc.arg(id)::uuid AND q.location_id = l.id AND l.project_id = p.id
  AND q.location_id = sqlc.arg(location_id)::uuid
  AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid
  AND q.source = 'manual';

-- name: DisableProjectLocationQueryForUser :one
UPDATE project_location_queries q SET enabled = FALSE, updated_at = now()
FROM project_locations l, projects p, organization_members m
WHERE q.id = sqlc.arg(id)::uuid AND q.location_id = l.id AND l.project_id = p.id
  AND q.location_id = sqlc.arg(location_id)::uuid
  AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid
  AND q.source = 'generated'
RETURNING q.*;

-- name: InsertMissingGeneratedProjectLocationQueryForUser :one
INSERT INTO project_location_queries(location_id, text, normalized, ordinal, enabled, kind, source, origin, landmark_id)
SELECT l.id, sqlc.arg(text)::text, sqlc.arg(normalized)::text, sqlc.arg(ordinal)::integer,
       TRUE, sqlc.arg(kind)::text, 'generated', sqlc.arg(origin)::text, sqlc.arg(landmark_id)::uuid
FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
  AND (sqlc.arg(landmark_id)::uuid IS NULL
       OR EXISTS (SELECT 1 FROM location_landmarks ll
                  WHERE ll.id = sqlc.arg(landmark_id)::uuid AND ll.location_id = l.id))
ON CONFLICT (location_id, kind, normalized) DO NOTHING
RETURNING *;

-- name: DeleteObsoleteGeneratedProjectLocationQueryForUser :execrows
DELETE FROM project_location_queries q
USING project_locations l, projects p, organization_members m
WHERE q.location_id = l.id AND l.project_id = p.id
  AND p.id = sqlc.arg(project_id)::uuid
  AND l.id = sqlc.arg(location_id)::uuid
  AND q.kind = sqlc.arg(kind)::text AND q.normalized = sqlc.arg(normalized)::text
  AND q.source = 'generated'
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid;

-- Handler runs this one via tx.Exec; sqlc generates no method, and the name is the UNIQUE (location_id, kind, ordinal) constraint.
SET CONSTRAINTS project_location_queries_location_id_kind_ordinal_key DEFERRED;
