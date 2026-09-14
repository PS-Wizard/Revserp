-- +goose Up
-- +goose StatementBegin

ALTER TABLE project_ai_questions
    ADD COLUMN location_questions JSONB;

CREATE TABLE maps_visibility_checks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    question TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    ll TEXT,
    zoom INTEGER,
    our_rank INTEGER,
    our_match_basis TEXT,
    credits_used INTEGER,
    error TEXT,
    results JSONB,
    listing JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX idx_maps_visibility_checks_project
    ON maps_visibility_checks(project_id, created_at DESC);
-- One in-flight run per project: double-clicking "test" cannot double-charge.
CREATE UNIQUE INDEX idx_maps_visibility_checks_inflight
    ON maps_visibility_checks(project_id)
    WHERE status IN ('queued', 'running');

CREATE TABLE maps_listing_refs (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    cid TEXT,
    place_id TEXT,
    title TEXT,
    website TEXT,
    address TEXT,
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS maps_listing_refs;
DROP INDEX IF EXISTS idx_maps_visibility_checks_inflight;
DROP TABLE IF EXISTS maps_visibility_checks;
ALTER TABLE project_ai_questions DROP COLUMN IF EXISTS location_questions;

-- +goose StatementEnd
