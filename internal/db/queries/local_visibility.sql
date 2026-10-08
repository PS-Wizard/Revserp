-- name: GetProjectLocationForUser :one
SELECT l.*, p.organization_id FROM project_locations l
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = $1 AND p.id = $2 AND m.user_id = $3;

-- name: ReservePlatformMapsCredits :one
UPDATE platform_maps_credit_budget SET reserved_credits = reserved_credits + sqlc.arg(credits)::bigint
WHERE id = TRUE AND remaining_credits - reserved_credits >= sqlc.arg(credits)::bigint
RETURNING *;

-- name: ReserveOrganizationMapsCredits :one
UPDATE organization_maps_credit_budgets SET reserved_credits = reserved_credits + sqlc.arg(credits)::bigint
WHERE organization_id = sqlc.arg(organization_id) AND remaining_credits - reserved_credits >= sqlc.arg(credits)::bigint
RETURNING *;

-- name: SettlePlatformMapsCredits :exec
UPDATE platform_maps_credit_budget
SET reserved_credits = reserved_credits - sqlc.arg(released)::bigint,
remaining_credits = remaining_credits - sqlc.arg(spent)::bigint,
spent_credits = spent_credits + sqlc.arg(spent)::bigint
WHERE id = TRUE;

-- name: SettleOrganizationMapsCredits :exec
UPDATE organization_maps_credit_budgets
SET reserved_credits = reserved_credits - sqlc.arg(released)::bigint,
remaining_credits = remaining_credits - sqlc.arg(spent)::bigint,
spent_credits = spent_credits + sqlc.arg(spent)::bigint
WHERE organization_id = sqlc.arg(organization_id);

-- name: CreateLocalVisibilityRun :one
INSERT INTO local_visibility_runs(location_id,radius_m,snapshot,expected_credits,reserved_credits)
VALUES($1,$2,$3,$4,$4) RETURNING *;

-- name: CreateLocalRunCells :exec
INSERT INTO local_run_cells(run_id,query_index,point_index)
SELECT $1, q::integer, p::smallint
FROM generate_series(0,sqlc.arg(query_count)::integer-1) q,
generate_series(0,sqlc.arg(point_count)::integer-1) p;

-- name: EnqueueLocalVisibilityJob :one
INSERT INTO ai_worker_jobs(job_type,project_id,local_run_id,status)
VALUES('local_visibility',$1,$2,'pending') RETURNING id;

-- name: GetLocalVisibilityRunForUser :one
SELECT r.* FROM local_visibility_runs r
JOIN project_locations l ON l.id = r.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE r.id = $1 AND l.id = $2 AND p.id = $3 AND m.user_id = $4;

-- name: GetLatestLocalVisibilityRunForUser :one
SELECT r.* FROM local_visibility_runs r
JOIN project_locations l ON l.id = r.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE l.id = $1 AND p.id = $2 AND m.user_id = $3
ORDER BY r.created_at DESC, r.id DESC LIMIT 1;

-- name: GetLocalVisibilityRun :one
SELECT r.*,p.organization_id,l.project_id FROM local_visibility_runs r
JOIN project_locations l ON l.id = r.location_id
JOIN projects p ON p.id = l.project_id
WHERE r.id = $1;

-- name: LockLocalVisibilityRun :one
SELECT * FROM local_visibility_runs WHERE id = $1 FOR UPDATE;

-- name: StartLocalVisibilityRun :one
UPDATE local_visibility_runs SET status = 'running',started_at = now()
WHERE id = $1 AND status = 'queued' RETURNING *;

-- name: StartLocalRunCell :one
UPDATE local_run_cells c SET started_at = now()
WHERE c.run_id = $1 AND c.query_index = $2 AND c.point_index = $3 AND c.started_at IS NULL
AND NOT EXISTS(SELECT 1 FROM local_visibility_results r WHERE r.run_id = c.run_id AND r.query_index = c.query_index AND r.point_index = c.point_index)
RETURNING *;

-- name: InsertLocalVisibilityResult :exec
INSERT INTO local_visibility_results(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known,raw_response,error)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);

-- name: SettleLocalRunCredits :exec
UPDATE local_visibility_runs SET reserved_credits = reserved_credits - sqlc.arg(released)::integer,
credits_used = credits_used + sqlc.arg(spent)::integer
WHERE id = sqlc.arg(id);

-- name: CountLocalVisibilityRunResultsForUser :one
SELECT COUNT(*)::bigint FROM local_visibility_results res
JOIN local_visibility_runs r ON r.id = res.run_id
JOIN project_locations l ON l.id = r.location_id
JOIN projects p ON p.id = l.project_id
JOIN organization_members m ON m.org_id = p.organization_id
WHERE res.run_id = $1 AND l.id = $2 AND p.id = $3 AND m.user_id = $4;

-- name: GetLocalRunCells :many
SELECT c.query_index,c.point_index,c.started_at,
COALESCE(r.call_status,'pending')::text AS call_status,
COALESCE(r.match_status,'unknown')::text AS match_status,
r.rank,COALESCE(r.credits,0)::integer AS credits,
COALESCE(r.credit_known,FALSE)::boolean AS credit_known,r.error,
COALESCE(r.raw_response->>'ll','')::text AS echoed_ll,
COALESCE((r.raw_response->>'viewport_drift_m')::double precision,-1)::double precision AS viewport_drift_m,
-- sqlc infers a nullable CASE as int32; -1 prevents NULL scans and becomes null in the API.
CASE WHEN r.call_status IN ('success_nonempty','success_empty') AND jsonb_typeof(r.raw_response->'places') = 'array' THEN jsonb_array_length(r.raw_response->'places') ELSE -1 END::integer AS result_count
FROM local_run_cells c LEFT JOIN local_visibility_results r
ON r.run_id = c.run_id AND r.query_index = c.query_index AND r.point_index = c.point_index
WHERE c.run_id = $1 ORDER BY c.query_index,c.point_index;

-- name: GetLocalVisibilityPointResults :many
SELECT c.query_index,
COALESCE(r.call_status,'pending')::text AS call_status,
COALESCE(r.match_status,'unknown')::text AS match_status,
r.rank,r.raw_response,r.error
FROM local_run_cells c LEFT JOIN local_visibility_results r
ON r.run_id = c.run_id AND r.query_index = c.query_index AND r.point_index = c.point_index
WHERE c.run_id = $1 AND c.point_index = $2 ORDER BY c.query_index;

-- name: GetLocalVisibilityRunCompetitorResults :many
SELECT c.query_index,c.point_index,
COALESCE(r.call_status,'pending')::text AS call_status,
r.raw_response
FROM local_run_cells c LEFT JOIN local_visibility_results r
ON r.run_id = c.run_id AND r.query_index = c.query_index AND r.point_index = c.point_index
WHERE c.run_id = $1 ORDER BY c.query_index,c.point_index;

-- name: FinishLocalVisibilityRun :exec
UPDATE local_visibility_runs SET status = $2,error = $3,completed_at = now() WHERE id = $1;

-- name: ListInterruptedLocalVisibilityRuns :many
SELECT r.id FROM local_visibility_runs r
JOIN ai_worker_jobs j ON j.local_run_id = r.id
WHERE j.status = 'failed' AND r.status IN ('queued','running');

-- name: GetOrganizationMapsCreditBudget :one
SELECT o.id AS organization_id,COALESCE(b.remaining_credits,0)::bigint AS remaining_credits,
COALESCE(b.reserved_credits,0)::bigint AS reserved_credits,COALESCE(b.spent_credits,0)::bigint AS spent_credits
FROM organizations o LEFT JOIN organization_maps_credit_budgets b ON b.organization_id = o.id
WHERE o.id = $1;

-- name: UpdateOrganizationMapsCreditBudget :one
INSERT INTO organization_maps_credit_budgets(organization_id,remaining_credits)
SELECT o.id,sqlc.arg(remaining_credits)::bigint FROM organizations o
LEFT JOIN organization_maps_credit_budgets b ON b.organization_id = o.id
WHERE o.id = sqlc.arg(organization_id) AND COALESCE(b.remaining_credits,0) = sqlc.arg(expected_remaining_credits)::bigint
ON CONFLICT(organization_id) DO UPDATE SET remaining_credits = EXCLUDED.remaining_credits
WHERE organization_maps_credit_budgets.remaining_credits = sqlc.arg(expected_remaining_credits)::bigint
RETURNING *;

-- name: GetPlatformMapsCreditBudget :one
SELECT * FROM platform_maps_credit_budget WHERE id = TRUE;

-- name: UpdatePlatformMapsCreditBudget :one
UPDATE platform_maps_credit_budget SET remaining_credits = sqlc.arg(remaining_credits)::bigint
WHERE id = TRUE AND remaining_credits = sqlc.arg(expected_remaining_credits)::bigint RETURNING *;
