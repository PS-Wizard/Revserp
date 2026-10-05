-- +goose Up
-- +goose StatementBegin
ALTER TABLE project_locations ALTER COLUMN place_id DROP NOT NULL;
ALTER TABLE project_locations DROP CONSTRAINT project_locations_queries_check;
ALTER TABLE project_locations ADD CONSTRAINT project_locations_queries_check
    CHECK (jsonb_typeof(queries) = 'array' AND jsonb_array_length(queries) BETWEEN 0 AND 5);
ALTER TABLE project_locations ADD COLUMN address TEXT NOT NULL DEFAULT '';
ALTER TABLE project_locations ADD COLUMN locality TEXT NOT NULL DEFAULT '';
ALTER TABLE project_locations ADD COLUMN query_service TEXT NOT NULL DEFAULT '';

CREATE TABLE location_geography_cache (
    cache_key TEXT PRIMARY KEY,
    results JSONB NOT NULL CHECK (jsonb_typeof(results) = 'array'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE local_listing_lookups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL REFERENCES project_locations(id) ON DELETE CASCADE,
    query TEXT NOT NULL CHECK (length(btrim(query)) > 0),
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed','uncertain')),
    expected_credits INTEGER NOT NULL DEFAULT 1 CHECK (expected_credits = 1),
    reserved_credits BIGINT NOT NULL DEFAULT 1 CHECK (reserved_credits BETWEEN 0 AND 1),
    credits_used BIGINT NOT NULL DEFAULT 0 CHECK (credits_used >= 0),
    credit_known BOOLEAN NOT NULL DEFAULT FALSE,
    raw_response JSONB,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX local_listing_lookups_latest_idx ON local_listing_lookups(location_id,created_at DESC);
CREATE UNIQUE INDEX local_listing_lookups_unsettled_idx ON local_listing_lookups(location_id)
    WHERE status = 'running' OR reserved_credits > 0;

CREATE FUNCTION protect_listing_lookup_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.location_id IS DISTINCT FROM OLD.location_id
       OR NEW.query IS DISTINCT FROM OLD.query
       OR NEW.expected_credits IS DISTINCT FROM OLD.expected_credits
       OR OLD.status <> 'running' THEN
        RAISE EXCEPTION 'listing lookup evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER listing_lookup_evidence_immutable BEFORE UPDATE ON local_listing_lookups
    FOR EACH ROW EXECUTE FUNCTION protect_listing_lookup_evidence();

CREATE OR REPLACE FUNCTION protect_location_unsettled_maps_spend() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM local_visibility_runs WHERE location_id = OLD.id
               AND (status IN ('queued','running') OR reserved_credits > 0))
       OR EXISTS (SELECT 1 FROM local_listing_lookups WHERE location_id = OLD.id
                  AND (status = 'running' OR reserved_credits > 0)) THEN
        RAISE EXCEPTION 'location has active or unconfirmed Maps spend; settle before deletion'
            USING ERRCODE = '23503';
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM project_locations WHERE place_id IS NULL OR jsonb_array_length(queries) <> 5)
       OR EXISTS (SELECT 1 FROM local_listing_lookups WHERE status = 'running' OR reserved_credits > 0) THEN
        RAISE EXCEPTION 'resolve locations and listing spend before removing location setup';
    END IF;
END $$;
CREATE OR REPLACE FUNCTION protect_location_unsettled_maps_spend() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM local_visibility_runs WHERE location_id = OLD.id
               AND (status IN ('queued','running') OR reserved_credits > 0)) THEN
        RAISE EXCEPTION 'location has active or unconfirmed Maps spend; settle before deletion'
            USING ERRCODE = '23503';
    END IF;
    RETURN OLD;
END;
$$;
DROP TABLE local_listing_lookups;
DROP FUNCTION protect_listing_lookup_evidence();
DROP TABLE location_geography_cache;
ALTER TABLE project_locations DROP COLUMN address, DROP COLUMN locality, DROP COLUMN query_service;
ALTER TABLE project_locations DROP CONSTRAINT project_locations_queries_check;
ALTER TABLE project_locations ADD CONSTRAINT project_locations_queries_check
    CHECK (jsonb_typeof(queries) = 'array' AND jsonb_array_length(queries) = 5);
ALTER TABLE project_locations ALTER COLUMN place_id SET NOT NULL;
-- +goose StatementEnd
