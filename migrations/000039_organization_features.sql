-- +goose Up
-- +goose StatementBegin
-- Per-workspace feature gating, administered from the platform admin page.
--
-- Denylist semantics: a workspace with no row here has every feature enabled.
-- A row exists only once an admin has restricted something. This keeps new
-- signups working without admin intervention, and means a newly shipped feature
-- or AI tool is live everywhere by default.
CREATE TABLE organization_features (
    org_id UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    auto_crawl BOOLEAN NOT NULL DEFAULT TRUE,
    gsc_connector BOOLEAN NOT NULL DEFAULT TRUE,
    ai_chat BOOLEAN NOT NULL DEFAULT TRUE,
    -- Individual tool names, not groups. The admin UI presents these grouped,
    -- but storing them per-tool keeps the group boundaries a presentation
    -- choice rather than a schema commitment.
    disabled_ai_tools TEXT[] NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS organization_features;
-- +goose StatementEnd
