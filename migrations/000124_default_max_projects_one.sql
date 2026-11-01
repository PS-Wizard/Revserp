-- +goose Up
-- +goose StatementBegin

-- New workspaces default to 1 project (was 5). A workspace with no
-- organization_features row reads the query fallback, so existing workspaces
-- without a row first get an explicit row that pins today's values (every
-- column matches the COALESCE fallbacks in organization_features.sql). That
-- keeps them at 5 projects; only workspaces created after this get 1.
INSERT INTO organization_features (
    org_id,
    auto_crawl,
    gsc_connector,
    ai_chat,
    integrations,
    ai_use_internal_prompt,
    ai_monthly_message_limit,
    ai_concurrent_turn_limit_per_user,
    ai_visibility_audit_monthly_limit,
    max_competitors,
    max_projects,
    ai_allowed_reasoning_efforts,
    disabled_ai_tools
)
SELECT
    o.id,
    TRUE,
    TRUE,
    TRUE,
    TRUE,
    FALSE,
    50,
    2,
    10,
    3,
    5,
    ARRAY['none', 'low', 'high', 'max']::TEXT[],
    ARRAY[]::TEXT[]
FROM organizations AS o
WHERE NOT EXISTS (
    SELECT 1 FROM organization_features AS f WHERE f.org_id = o.id
);

ALTER TABLE organization_features ALTER COLUMN max_projects SET DEFAULT 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The pinned rows match the old fallbacks exactly, so they stay.
ALTER TABLE organization_features ALTER COLUMN max_projects SET DEFAULT 5;
-- +goose StatementEnd
