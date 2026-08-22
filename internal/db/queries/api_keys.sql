-- name: CreateAPIKey :one
INSERT INTO api_keys (user_id, name, token_prefix, token_hash)
VALUES ($1, $2, $3, $4)
RETURNING id, user_id, name, token_prefix, created_at, last_used_at, revoked_at;

-- name: ListAPIKeysForUser :many
SELECT id, name, token_prefix, created_at, last_used_at, revoked_at
FROM api_keys
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetActiveAPIKeyWithUserByTokenHash :one
SELECT
    ak.id AS api_key_id,
    ak.user_id,
    ak.token_prefix,
    ak.last_used_at,
    u.auth_provider,
    u.auth_subject,
    u.email,
    u.name,
    u.status
FROM api_keys AS ak
INNER JOIN users AS u ON u.id = ak.user_id
WHERE ak.token_hash = $1
  AND ak.revoked_at IS NULL
LIMIT 1;

-- name: TouchAPIKeyLastUsedAt :execrows
UPDATE api_keys
SET last_used_at = now()
WHERE id = $1
  AND revoked_at IS NULL
  AND (last_used_at IS NULL OR last_used_at <= now() - interval '5 minutes');

-- name: RevokeAPIKeyForUser :execrows
UPDATE api_keys
SET revoked_at = COALESCE(revoked_at, now())
WHERE id = $1
  AND user_id = $2;

-- name: CreateAgentSetupCode :one
INSERT INTO agent_setup_codes (user_id, name, code_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id, user_id, name, expires_at, redeemed_at, created_at;

-- name: GetAgentSetupCodeForUpdate :one
SELECT
    ascodes.id,
    ascodes.user_id,
    ascodes.name,
    ascodes.expires_at,
    ascodes.redeemed_at,
    u.status AS user_status
FROM agent_setup_codes AS ascodes
INNER JOIN users AS u ON u.id = ascodes.user_id
WHERE ascodes.code_hash = $1
FOR UPDATE OF ascodes;

-- name: MarkAgentSetupCodeRedeemed :execrows
UPDATE agent_setup_codes
SET redeemed_at = now()
WHERE id = $1
  AND redeemed_at IS NULL;

-- name: DeleteExpiredAgentSetupCodes :execrows
DELETE FROM agent_setup_codes
WHERE expires_at < now();
