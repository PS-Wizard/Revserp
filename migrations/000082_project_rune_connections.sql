-- +goose Up
CREATE TABLE project_rune_connections (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    endpoint_url TEXT NOT NULL,
    encrypted_token TEXT NOT NULL,
    revision UUID NOT NULL DEFAULT gen_random_uuid(),
    tools JSONB NOT NULL DEFAULT '[]',
    last_checked_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS project_rune_connections;
