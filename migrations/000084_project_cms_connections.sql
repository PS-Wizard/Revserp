-- Forward-migrates the single active CMS connection per project from Rune-only
-- to provider-tagged storage. Existing rows keep their endpoint, encrypted
-- credentials, revision, tools, and timestamps; the provider backfills to
-- 'rune' so deployed Rune connections keep working unchanged.
-- Historical migration 82 stays unchanged.

-- +goose Up
ALTER TABLE project_rune_connections RENAME TO project_cms_connections;
ALTER TABLE project_cms_connections
    ADD COLUMN provider TEXT NOT NULL DEFAULT 'rune'
        CHECK (provider IN ('rune', 'wordpress'));

-- +goose Down
ALTER TABLE project_cms_connections
    DROP COLUMN provider;
ALTER TABLE project_cms_connections RENAME TO project_rune_connections;
