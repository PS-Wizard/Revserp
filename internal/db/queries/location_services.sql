-- name: ListProjectServicesForUser :many
SELECT s.* FROM project_services s
JOIN projects p ON p.id = s.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE s.project_id = sqlc.arg(project_id)::uuid AND m.user_id = sqlc.arg(user_id)::uuid
ORDER BY s.normalized_label, s.id;

-- name: CreateProjectServiceForUser :one
INSERT INTO project_services(project_id, label, normalized_label)
SELECT p.id, sqlc.arg(label)::text, sqlc.arg(normalized_label)::text
FROM projects p
JOIN organization_members m ON m.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id)::uuid AND m.user_id = sqlc.arg(user_id)::uuid
RETURNING *;

-- name: RenameProjectServiceForUser :one
UPDATE project_services s SET label = sqlc.arg(label)::text,
    normalized_label = sqlc.arg(normalized_label)::text, updated_at = now()
FROM projects p, organization_members m
WHERE s.id = sqlc.arg(service_id)::uuid AND s.project_id = p.id
  AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid
RETURNING s.*;

-- name: DeleteProjectServiceForUser :execrows
DELETE FROM project_services s
USING projects p, organization_members m
WHERE s.id = sqlc.arg(service_id)::uuid AND s.project_id = p.id
  AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid;

-- name: ListProjectLocationServiceEditorRowsForUser :many
SELECT s.id AS service_id, s.label, s.normalized_label,
    (o.id IS NOT NULL)::boolean AS excluded
FROM project_services s
JOIN projects p ON p.id = s.project_id
JOIN organization_members m ON m.org_id = p.organization_id
JOIN project_locations l
  ON l.id = sqlc.arg(location_id)::uuid AND l.project_id = p.id
LEFT JOIN project_location_services o
  ON o.location_id = l.id AND o.service_id = s.id AND o.mode = 'exclude'
WHERE s.project_id = sqlc.arg(project_id)::uuid AND m.user_id = sqlc.arg(user_id)::uuid
ORDER BY s.normalized_label, s.id;

-- name: ListProjectLocationOnlyServiceLabelsForUser :many
SELECT o.id, o.service_label, o.normalized_service_label
FROM project_location_services o
JOIN project_locations l ON l.id = o.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE o.location_id = sqlc.arg(location_id)::uuid
  AND l.project_id = sqlc.arg(project_id)::uuid
  AND o.service_label IS NOT NULL AND o.mode = 'include'
  AND m.user_id = sqlc.arg(user_id)::uuid
ORDER BY o.normalized_service_label, o.id;

-- name: ListEffectiveProjectLocationServiceLabelsForUser :many
SELECT label, normalized_label
FROM (
    SELECT DISTINCT ON (normalized_label) label, normalized_label, bucket
    FROM (
        SELECT s.label, s.normalized_label, 0 AS bucket
        FROM project_services s
        JOIN project_locations l
          ON l.id = sqlc.arg(location_id)::uuid AND l.project_id = s.project_id
        JOIN projects p ON p.id = l.project_id
        JOIN organization_members m ON m.org_id = p.organization_id
        WHERE s.project_id = sqlc.arg(project_id)::uuid
          AND m.user_id = sqlc.arg(user_id)::uuid
          AND NOT EXISTS (
              SELECT 1 FROM project_location_services o
              WHERE o.location_id = l.id AND o.service_id = s.id AND o.mode = 'exclude'
          )
        UNION ALL
        SELECT o.service_label, o.normalized_service_label, 1 AS bucket
        FROM project_location_services o
        JOIN project_locations l ON l.id = o.location_id
        JOIN projects p ON p.id = l.project_id
        JOIN organization_members m ON m.org_id = p.organization_id
        WHERE o.location_id = sqlc.arg(location_id)::uuid
          AND l.project_id = sqlc.arg(project_id)::uuid
          AND o.service_label IS NOT NULL AND o.mode = 'include'
          AND m.user_id = sqlc.arg(user_id)::uuid
    ) combined
    ORDER BY normalized_label, bucket
) deduped
ORDER BY bucket, normalized_label;

-- name: DeleteProjectLocationServiceOverridesForUser :execrows
DELETE FROM project_location_services o
USING project_locations l, projects p, organization_members m
WHERE o.location_id = l.id AND l.project_id = p.id
  AND l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.org_id = p.organization_id AND m.user_id = sqlc.arg(user_id)::uuid;

-- name: InsertProjectLocationServiceOverrideForUser :one
INSERT INTO project_location_services(location_id, project_id, service_id, service_label, normalized_service_label, mode)
SELECT l.id, l.project_id, sqlc.arg(service_id)::uuid, sqlc.narg(service_label)::text,
       sqlc.narg(normalized_service_label)::text, sqlc.arg(mode)::text
FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = sqlc.arg(location_id)::uuid AND p.id = sqlc.arg(project_id)::uuid
  AND m.user_id = sqlc.arg(user_id)::uuid
RETURNING *;
