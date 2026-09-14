-- name: CreateMapsVisibilityCheck :one
INSERT INTO maps_visibility_checks (project_id, question, status)
VALUES ($1, $2, 'queued')
RETURNING *;

-- name: GetMapsVisibilityCheck :one
SELECT * FROM maps_visibility_checks WHERE id = $1;

-- name: GetLatestMapsVisibilityCheckByProject :one
SELECT * FROM maps_visibility_checks
WHERE project_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: MarkMapsVisibilityCheckRunning :exec
UPDATE maps_visibility_checks
SET status = 'running', updated_at = NOW()
WHERE id = $1;

-- name: MarkMapsVisibilityCheckCompleted :exec
UPDATE maps_visibility_checks
SET status = 'completed',
    ll = $2,
    zoom = $3,
    our_rank = $4,
    our_match_basis = $5,
    credits_used = $6,
    results = $7,
    listing = $8,
    completed_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: MarkMapsVisibilityCheckFailed :exec
UPDATE maps_visibility_checks
SET status = 'failed', error = $2, completed_at = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: GetMapsVisibilityCheckInFlight :one
SELECT * FROM maps_visibility_checks
WHERE project_id = $1 AND status IN ('queued', 'running')
LIMIT 1;

-- name: UpsertMapsListingRef :one
INSERT INTO maps_listing_refs (project_id, cid, place_id, title, website, address, resolved_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
ON CONFLICT (project_id) DO UPDATE SET
    cid = EXCLUDED.cid,
    place_id = EXCLUDED.place_id,
    title = EXCLUDED.title,
    website = EXCLUDED.website,
    address = EXCLUDED.address,
    resolved_at = NOW(),
    updated_at = NOW()
RETURNING project_id, cid, place_id, title, website, address, resolved_at, updated_at;

-- name: GetMapsListingRef :one
SELECT project_id, cid, place_id, title, website, address, resolved_at, updated_at
FROM maps_listing_refs
WHERE project_id = $1;
