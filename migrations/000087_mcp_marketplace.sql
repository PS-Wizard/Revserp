-- MCP marketplace storage: multiple generic project connections with
-- per-tool user policy, replacing the single active CMS connection.
-- Credentials, endpoints, revisions, discovered tools and timestamps migrate
-- unchanged; wordpress stays wordpress, rune becomes a custom connection
-- named Rune CMS with no special protocol. Absence of a permission row means
-- Ask; explicit Deny rows survive rediscovery. Historical migrations stay
-- unchanged.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE project_mcp_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(btrim(name)) > 0 AND length(name) <= 100),
    service TEXT NOT NULL CHECK (service IN ('wordpress', 'custom')),
    endpoint_url TEXT NOT NULL CHECK (length(btrim(endpoint_url)) > 0),
    encrypted_token TEXT NOT NULL CHECK (length(encrypted_token) > 0),
    revision UUID NOT NULL DEFAULT gen_random_uuid(),
    tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX idx_project_mcp_connections_project
    ON project_mcp_connections(project_id);

CREATE TABLE project_mcp_tool_permissions (
    connection_id UUID NOT NULL REFERENCES project_mcp_connections(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL CHECK (length(btrim(tool_name)) > 0),
    permission TEXT NOT NULL CHECK (permission IN ('ask', 'allow', 'deny')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, tool_name)
);

INSERT INTO project_mcp_connections (
    project_id, name, service, endpoint_url, encrypted_token, revision,
    tools, last_checked_at, created_at, updated_at
)
SELECT project_id,
    CASE WHEN provider = 'rune' THEN 'Rune CMS' ELSE 'WordPress' END,
    CASE WHEN provider = 'rune' THEN 'custom' ELSE 'wordpress' END,
    endpoint_url, encrypted_token, revision,
    tools, last_checked_at, created_at, updated_at
FROM project_cms_connections;

ALTER TABLE ai_cms_approvals RENAME TO ai_mcp_approvals;
ALTER TABLE ai_mcp_approvals
    DROP CONSTRAINT IF EXISTS ai_cms_approvals_provider_check;
ALTER TABLE ai_mcp_approvals
    ADD COLUMN connection_id UUID REFERENCES project_mcp_connections(id) ON DELETE SET NULL,
    ADD COLUMN connection_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN remote_tool_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN schema_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN service TEXT NOT NULL DEFAULT '';
UPDATE ai_mcp_approvals SET service = provider WHERE service = '';
UPDATE ai_mcp_approvals
SET remote_tool_name = regexp_replace(tool_name, '^(cms__|wp__)', '')
WHERE remote_tool_name = '';
UPDATE ai_mcp_approvals AS a
SET connection_id = c.id, connection_name = c.name
FROM ai_turns AS t
JOIN ai_conversations AS conv ON conv.id = t.conversation_id
JOIN project_mcp_connections AS c ON c.project_id = conv.project_id
WHERE a.turn_id = t.id AND a.connection_id IS NULL AND c.service = a.service;

-- Retired cms__/wp__ denylist entries were compared against the old single
-- connection's model names. The generic model alias is
-- 'mcp_' || connection UUID without hyphens || '_' || first 16 hex chars of
-- sha256(exact remote name); Postgres core sha256/encode need no extension.
-- Append the generic alias next to the preserved native and historical
-- entries, matching only the connection whose service owns that namespace.
UPDATE organization_features AS f
SET disabled_ai_tools = f.disabled_ai_tools || m.aliases
FROM (
    SELECT f2.org_id AS org_id,
        array_agg(DISTINCT 'mcp_' || replace(c.id::text, '-', '') || '_' ||
            substr(encode(sha256(convert_to(regexp_replace(a.name, '^(cms__|wp__)', ''), 'UTF8')), 'hex'), 1, 16)) AS aliases
    FROM organization_features AS f2
    CROSS JOIN LATERAL unnest(f2.disabled_ai_tools) AS a(name)
    JOIN projects AS p ON p.organization_id = f2.org_id
    JOIN project_mcp_connections AS c ON c.project_id = p.id
    WHERE a.name NOT IN ('cms__', 'wp__')
      AND ((left(a.name, 5) = 'cms__' AND c.service = 'custom')
        OR (left(a.name, 4) = 'wp__' AND c.service = 'wordpress'))
    GROUP BY f2.org_id
) AS m
WHERE f.org_id = m.org_id;

-- Active queued/running/waiting turns keep their own snapshot, so the same
-- project-specific conversion runs before those snapshots execute. Terminal
-- historical snapshots stay byte-for-byte unchanged.
UPDATE ai_turns AS t
SET disabled_ai_tools = t.disabled_ai_tools || m.aliases
FROM (
    SELECT t2.id AS turn_id,
        array_agg(DISTINCT 'mcp_' || replace(c.id::text, '-', '') || '_' ||
            substr(encode(sha256(convert_to(regexp_replace(a.name, '^(cms__|wp__)', ''), 'UTF8')), 'hex'), 1, 16)) AS aliases
    FROM ai_turns AS t2
    JOIN ai_conversations AS conv ON conv.id = t2.conversation_id
    JOIN projects AS p ON p.id = conv.project_id
    JOIN project_mcp_connections AS c ON c.project_id = p.id
    CROSS JOIN LATERAL unnest(t2.disabled_ai_tools) AS a(name)
    WHERE t2.status IN ('queued', 'running', 'waiting', 'waiting_for_user')
      AND a.name NOT IN ('cms__', 'wp__')
      AND ((left(a.name, 5) = 'cms__' AND c.service = 'custom')
        OR (left(a.name, 4) = 'wp__' AND c.service = 'wordpress'))
    GROUP BY t2.id
) AS m
WHERE t.id = m.turn_id;

DROP TABLE project_cms_connections;
-- +goose StatementEnd

-- +goose Down
-- Migration 087 has no automatic rollback: the forward migration merges
-- multiple generic connections (and their credentials, permissions, and
-- approval links) into shapes the single-connection legacy tables cannot
-- represent, so a Down would silently destroy data. Roll back manually
-- from a pre-migration backup instead.
DO $$ BEGIN
    RAISE EXCEPTION 'migration 087 (mcp marketplace) is irreversible: restore the pre-migration backup instead of rolling back';
END $$;
