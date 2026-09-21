-- +goose Up
CREATE TABLE project_google_analytics_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    google_connection_id UUID NOT NULL REFERENCES google_connections(id) ON DELETE CASCADE,
    property_id TEXT NOT NULL CHECK (btrim(property_id) <> ''),
    property_display_name TEXT NOT NULL CHECK (btrim(property_display_name) <> ''),
    account_display_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_google_analytics_connections_google_connection_id
    ON project_google_analytics_connections(google_connection_id);

-- +goose Down
DROP TABLE IF EXISTS project_google_analytics_connections;
