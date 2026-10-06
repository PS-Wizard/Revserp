-- +goose Up
-- +goose StatementBegin
-- Pure DDL: creates the Layer 4 tables and the localities ladder column. No data
-- backfill lives here; the consumer-switch migration owns the legacy reconcile.
ALTER TABLE project_locations ADD COLUMN localities JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(localities) = 'array');

ALTER TABLE project_locations ADD CONSTRAINT project_locations_id_project_key UNIQUE (id, project_id);

CREATE TABLE project_services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label TEXT NOT NULL CHECK (length(btrim(label)) > 0),
    normalized_label TEXT NOT NULL CHECK (length(btrim(normalized_label)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, normalized_label),
    UNIQUE (id, project_id)
);

CREATE TABLE project_location_services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL,
    project_id UUID NOT NULL,
    service_id UUID,
    service_label TEXT,
    normalized_service_label TEXT,
    mode TEXT NOT NULL CHECK (mode IN ('include', 'exclude')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((service_id IS NULL) <> (service_label IS NULL)),
    CHECK ((service_label IS NULL) = (normalized_service_label IS NULL)),
    CHECK (service_label IS NULL
           OR (length(btrim(service_label)) > 0
               AND length(btrim(normalized_service_label)) > 0)),
    CHECK (service_label IS NULL OR mode = 'include'),
    FOREIGN KEY (location_id, project_id)
        REFERENCES project_locations(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (service_id, project_id)
        REFERENCES project_services(id, project_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX project_location_services_service_key
    ON project_location_services(location_id, service_id) WHERE service_id IS NOT NULL;
CREATE UNIQUE INDEX project_location_services_label_key
    ON project_location_services(location_id, normalized_service_label) WHERE service_label IS NOT NULL;

CREATE TABLE location_landmarks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL REFERENCES project_locations(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(btrim(name)) > 0),
    latitude DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    straight_line_m INTEGER NOT NULL CHECK (straight_line_m >= 0),
    driving_m INTEGER NOT NULL CHECK (driving_m BETWEEN 0 AND 2000),
    provider TEXT NOT NULL CHECK (length(btrim(provider)) > 0),
    provider_ref TEXT NOT NULL CHECK (length(btrim(provider_ref)) > 0),
    categories TEXT[] NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    selected BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (location_id, provider_ref)
);

CREATE TABLE project_location_queries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL REFERENCES project_locations(id) ON DELETE CASCADE,
    text TEXT NOT NULL CHECK (length(btrim(text)) > 0),
    normalized TEXT NOT NULL CHECK (length(btrim(normalized)) > 0),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    kind TEXT NOT NULL CHECK (kind IN ('map', 'ai_question')),
    source TEXT NOT NULL CHECK (source IN ('generated', 'manual')),
    origin TEXT NOT NULL CHECK (origin IN ('service', 'locality', 'landmark')),
    landmark_id UUID REFERENCES location_landmarks(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (location_id, kind, normalized),
    UNIQUE (location_id, kind, ordinal) DEFERRABLE INITIALLY IMMEDIATE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE project_location_queries;
DROP TABLE location_landmarks;
DROP TABLE project_location_services;
DROP TABLE project_services;
ALTER TABLE project_locations DROP CONSTRAINT project_locations_id_project_key;
ALTER TABLE project_locations DROP COLUMN localities;
-- +goose StatementEnd
