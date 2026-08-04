-- Every read below LEFT JOINs and COALESCEs to the enabled default, so a
-- workspace with no organization_features row resolves to all-features-on
-- without the caller needing to distinguish "no row" from "everything allowed".

-- name: GetOrganizationFeatures :one
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.disabled_ai_tools, '{}')::text[] AS disabled_ai_tools
FROM organizations AS o
LEFT JOIN organization_features AS f ON f.org_id = o.id
WHERE o.id = sqlc.arg(org_id);

-- name: GetOrganizationFeaturesByProjectID :one
-- Resolves a project-scoped route to its workspace's features in one round trip.
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.disabled_ai_tools, '{}')::text[] AS disabled_ai_tools
FROM projects AS p
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE p.id = sqlc.arg(project_id);

-- name: GetOrganizationFeaturesByConversationID :one
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.disabled_ai_tools, '{}')::text[] AS disabled_ai_tools
FROM ai_conversations AS c
LEFT JOIN organization_features AS f ON f.org_id = c.org_id
WHERE c.id = sqlc.arg(conversation_id);

-- name: GetOrganizationFeaturesByCrawlID :one
SELECT
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.disabled_ai_tools, '{}')::text[] AS disabled_ai_tools
FROM crawls AS c
INNER JOIN projects AS p ON p.id = c.project_id
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE c.id = sqlc.arg(crawl_id);

-- name: ListOrganizationFeaturesForAdmin :many
-- Backs the admin matrix: every workspace, whether or not it has been restricted.
SELECT
    o.id AS org_id,
    o.name AS org_name,
    COALESCE(f.auto_crawl, TRUE)::boolean AS auto_crawl,
    COALESCE(f.gsc_connector, TRUE)::boolean AS gsc_connector,
    COALESCE(f.ai_chat, TRUE)::boolean AS ai_chat,
    COALESCE(f.disabled_ai_tools, '{}')::text[] AS disabled_ai_tools,
    f.updated_at
FROM organizations AS o
LEFT JOIN organization_features AS f ON f.org_id = o.id
ORDER BY o.name ASC;

-- name: UpsertOrganizationFeatures :exec
INSERT INTO organization_features (
    org_id, auto_crawl, gsc_connector, ai_chat, disabled_ai_tools, updated_by_user_id, updated_at
) VALUES (
    sqlc.arg(org_id),
    sqlc.arg(auto_crawl),
    sqlc.arg(gsc_connector),
    sqlc.arg(ai_chat),
    sqlc.arg(disabled_ai_tools),
    sqlc.narg(updated_by_user_id),
    now()
)
ON CONFLICT (org_id) DO UPDATE SET
    auto_crawl = EXCLUDED.auto_crawl,
    gsc_connector = EXCLUDED.gsc_connector,
    ai_chat = EXCLUDED.ai_chat,
    disabled_ai_tools = EXCLUDED.disabled_ai_tools,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = now();
