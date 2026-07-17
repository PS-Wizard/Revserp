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
