-- +goose Up
-- +goose StatementBegin
-- Multiple org-owned Google accounts (verified by Google subject) plus per-location
-- service bindings. Existing connection IDs and project property bindings are kept.
-- Account rows are revoked, never deleted, so project/location bindings keep working.
-- Account foreign keys defer checks to commit because tenant deletion reaches
-- location bindings after shared accounts. A direct account delete still fails
-- if any binding remains.
ALTER TABLE google_connections DROP CONSTRAINT IF EXISTS google_connections_organization_id_key;

CREATE UNIQUE INDEX IF NOT EXISTS google_connections_org_subject_uidx
    ON google_connections(organization_id, google_account_subject)
    WHERE google_account_subject IS NOT NULL AND btrim(google_account_subject) <> '';

ALTER TABLE google_oauth_states
    ADD COLUMN IF NOT EXISTS google_connection_id UUID REFERENCES google_connections(id) ON DELETE CASCADE;
ALTER TABLE google_oauth_states
    ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT 'connect'
    CHECK (purpose IN ('connect', 'add_account', 'reconnect_account'));

ALTER TABLE project_gsc_connections
    DROP CONSTRAINT IF EXISTS project_gsc_connections_google_connection_id_fkey;
ALTER TABLE project_gsc_connections
    ADD CONSTRAINT project_gsc_connections_google_connection_id_fkey
    FOREIGN KEY (google_connection_id) REFERENCES google_connections(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE project_google_analytics_connections
    DROP CONSTRAINT IF EXISTS project_google_analytics_connections_google_connection_id_fkey;
ALTER TABLE project_google_analytics_connections
    ADD CONSTRAINT project_google_analytics_connections_google_connection_id_fkey
    FOREIGN KEY (google_connection_id) REFERENCES google_connections(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE location_gsc_connections (
    location_id UUID PRIMARY KEY REFERENCES project_locations(id) ON DELETE CASCADE,
    mode TEXT NOT NULL DEFAULT 'inherit' CHECK (mode IN ('inherit', 'off', 'custom')),
    google_connection_id UUID REFERENCES google_connections(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    site_url TEXT CHECK (site_url IS NULL OR length(btrim(site_url)) > 0),
    permission_level TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (mode = 'custom' AND google_connection_id IS NOT NULL AND site_url IS NOT NULL)
        OR (mode <> 'custom' AND google_connection_id IS NULL AND site_url IS NULL AND permission_level IS NULL)
    )
);

CREATE TABLE location_google_analytics_connections (
    location_id UUID PRIMARY KEY REFERENCES project_locations(id) ON DELETE CASCADE,
    mode TEXT NOT NULL DEFAULT 'inherit' CHECK (mode IN ('inherit', 'off', 'custom')),
    google_connection_id UUID REFERENCES google_connections(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    property_id TEXT CHECK (property_id IS NULL OR length(btrim(property_id)) > 0),
    property_display_name TEXT,
    account_display_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (mode = 'custom' AND google_connection_id IS NOT NULL AND property_id IS NOT NULL)
        OR (mode <> 'custom' AND google_connection_id IS NULL AND property_id IS NULL
            AND property_display_name IS NULL AND account_display_name IS NULL)
    )
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS location_google_analytics_connections;
DROP TABLE IF EXISTS location_gsc_connections;
ALTER TABLE project_google_analytics_connections
    DROP CONSTRAINT IF EXISTS project_google_analytics_connections_google_connection_id_fkey;
ALTER TABLE project_google_analytics_connections
    ADD CONSTRAINT project_google_analytics_connections_google_connection_id_fkey
    FOREIGN KEY (google_connection_id) REFERENCES google_connections(id) ON DELETE CASCADE;
ALTER TABLE project_gsc_connections
    DROP CONSTRAINT IF EXISTS project_gsc_connections_google_connection_id_fkey;
ALTER TABLE project_gsc_connections
    ADD CONSTRAINT project_gsc_connections_google_connection_id_fkey
    FOREIGN KEY (google_connection_id) REFERENCES google_connections(id) ON DELETE CASCADE;
ALTER TABLE google_oauth_states DROP COLUMN IF EXISTS purpose;
ALTER TABLE google_oauth_states DROP COLUMN IF EXISTS google_connection_id;
DROP INDEX IF EXISTS google_connections_org_subject_uidx;
-- Down cannot restore UNIQUE (organization_id): multiple accounts per org may exist.
-- +goose StatementEnd
