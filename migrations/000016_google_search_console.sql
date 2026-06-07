-- +goose Up
-- +goose StatementBegin
CREATE TABLE google_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL UNIQUE REFERENCES organizations(id) ON DELETE CASCADE,
    connected_by_user_id UUID NOT NULL REFERENCES users(id),
    google_account_email TEXT,
    google_account_subject TEXT,
    encrypted_refresh_token TEXT NOT NULL,
    encrypted_access_token TEXT,
    access_token_expires_at TIMESTAMPTZ,
    scope TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'reauth_required', 'revoked')) DEFAULT 'active',
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE project_gsc_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    google_connection_id UUID NOT NULL REFERENCES google_connections(id) ON DELETE CASCADE,
    site_url TEXT NOT NULL,
    permission_level TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE google_oauth_states (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    state_token_hash TEXT NOT NULL UNIQUE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    return_path TEXT NOT NULL DEFAULT '/',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (left(return_path, 1) = '/')
);

CREATE INDEX idx_google_connections_status ON google_connections(status);
CREATE INDEX idx_project_gsc_connections_google_connection_id ON project_gsc_connections(google_connection_id);
CREATE INDEX idx_google_oauth_states_expires_at ON google_oauth_states(expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS google_oauth_states;
DROP TABLE IF EXISTS project_gsc_connections;
DROP TABLE IF EXISTS google_connections;
-- +goose StatementEnd
