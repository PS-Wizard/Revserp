-- A workspace with no organization_features row resolves to all features on.

-- name: GetOrganizationFeatures :one
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.ai_monthly_message_limit, 50)::integer AS ai_monthly_message_limit,
    COALESCE(
        f.ai_allowed_reasoning_efforts,
        ARRAY['none', 'low', 'high', 'max']::TEXT[]
    ) AS ai_allowed_reasoning_efforts
FROM organizations AS o
LEFT JOIN organization_features AS f ON f.org_id = o.id
WHERE o.id = sqlc.arg(org_id);

-- name: GetOrganizationFeaturesByProjectID :one
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.ai_monthly_message_limit, 50)::integer AS ai_monthly_message_limit,
    COALESCE(
        f.ai_allowed_reasoning_efforts,
        ARRAY['none', 'low', 'high', 'max']::TEXT[]
    ) AS ai_allowed_reasoning_efforts
FROM projects AS p
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id);

-- name: GetOrganizationFeaturesByConversationID :one
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.ai_monthly_message_limit, 50)::integer AS ai_monthly_message_limit,
    COALESCE(
        f.ai_allowed_reasoning_efforts,
        ARRAY['none', 'low', 'high', 'max']::TEXT[]
    ) AS ai_allowed_reasoning_efforts
FROM ai_conversations AS ac
INNER JOIN projects AS p ON p.id = ac.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
    AND om.user_id = sqlc.arg(user_id)
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE ac.id = sqlc.arg(conversation_id);

-- name: ListOrganizationFeaturesForAdmin :many
SELECT
    o.id AS org_id,
    o.name AS org_name,
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.ai_monthly_message_limit, 50)::integer AS ai_monthly_message_limit,
    COALESCE(
        f.ai_allowed_reasoning_efforts,
        ARRAY['none', 'low', 'high', 'max']::TEXT[]
    ) AS ai_allowed_reasoning_efforts,
    f.updated_at
FROM organizations AS o
LEFT JOIN organization_features AS f ON f.org_id = o.id
ORDER BY o.name ASC;

-- name: UpsertOrganizationFeatures :exec
INSERT INTO organization_features (
    org_id, auto_crawl, gsc_connector, ai_chat,
    ai_monthly_message_limit, ai_allowed_reasoning_efforts,
    updated_by_user_id, updated_at
) VALUES (
    sqlc.arg(org_id),
    sqlc.arg(auto_crawl),
    sqlc.arg(gsc_connector),
    sqlc.arg(ai_chat),
    sqlc.arg(ai_monthly_message_limit),
    ARRAY(
        SELECT effort
        FROM unnest(sqlc.arg(ai_allowed_reasoning_efforts)::TEXT[]) AS effort
        ORDER BY array_position(ARRAY['none', 'low', 'high', 'max']::TEXT[], effort)
    ),
    sqlc.narg(updated_by_user_id),
    now()
)
ON CONFLICT (org_id) DO UPDATE SET
    auto_crawl = EXCLUDED.auto_crawl,
    gsc_connector = EXCLUDED.gsc_connector,
    ai_chat = EXCLUDED.ai_chat,
    ai_monthly_message_limit = EXCLUDED.ai_monthly_message_limit,
    ai_allowed_reasoning_efforts = EXCLUDED.ai_allowed_reasoning_efforts,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = now();
