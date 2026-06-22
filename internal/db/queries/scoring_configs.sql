-- name: GetActiveScoringConfig :one
SELECT id, config_json, updated_by_user_id, updated_at
FROM scoring_configs
WHERE id = 1;

-- name: UpsertActiveScoringConfig :one
INSERT INTO scoring_configs (
    id,
    config_json,
    updated_by_user_id
) VALUES (
    1,
    $1,
    $2
)
ON CONFLICT (id) DO UPDATE SET
    config_json = EXCLUDED.config_json,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = NOW()
RETURNING id, config_json, updated_by_user_id, updated_at;

-- name: GetOrgScoringConfig :one
SELECT org_id, config_json, updated_by_user_id, updated_at
FROM organization_scoring_configs
WHERE org_id = $1
LIMIT 1;

-- name: UpsertOrgScoringConfig :one
INSERT INTO organization_scoring_configs (
    org_id,
    config_json,
    updated_by_user_id
) VALUES (
    $1,
    $2,
    $3
)
ON CONFLICT (org_id) DO UPDATE SET
    config_json = EXCLUDED.config_json,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = NOW()
RETURNING org_id, config_json, updated_by_user_id, updated_at;

-- name: DeleteOrgScoringConfig :exec
DELETE FROM organization_scoring_configs WHERE org_id = $1;
