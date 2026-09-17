-- name: ListOrganizationEventsForUser :many
SELECT
    oe.id,
    oe.organization_id,
    oe.project_id,
    oe.event_type,
    oe.resource_id,
    oe.payload,
    oe.created_at
FROM organization_events AS oe
INNER JOIN organization_members AS om
    ON om.org_id = oe.organization_id
   AND om.user_id = sqlc.arg(user_id)
WHERE oe.organization_id = sqlc.arg(organization_id)
  AND oe.id > sqlc.arg(after_id)
ORDER BY oe.id ASC
LIMIT sqlc.arg(max_rows);

-- name: GetOrganizationEventHeadForUser :one
SELECT COALESCE(MAX(oe.id), 0)::bigint AS head
FROM organization_events AS oe
INNER JOIN organization_members AS om
    ON om.org_id = oe.organization_id
   AND om.user_id = sqlc.arg(user_id)
WHERE oe.organization_id = sqlc.arg(organization_id);

-- name: DeleteOrganizationEventsBefore :execrows
DELETE FROM organization_events
WHERE id IN (
    SELECT oe.id FROM organization_events AS oe
    WHERE oe.created_at < sqlc.arg(cutoff)
    ORDER BY oe.id ASC
    LIMIT sqlc.arg(batch_size)
);

-- name: TryOrganizationEventsCleanupLock :one
SELECT pg_try_advisory_xact_lock(918273645, 1) AS locked;