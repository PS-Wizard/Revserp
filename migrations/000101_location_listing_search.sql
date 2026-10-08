-- +goose Up
-- +goose StatementBegin
-- Project-scoped Places listing searches for the bound-location flow. Search
-- is not a location: evidence lives here with project/user ownership and an
-- expiry, and carries zero application credits. Existing local_listing_lookups
-- rows and Maps budgets are untouched.
CREATE TABLE location_listing_searches (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    query TEXT NOT NULL CHECK (length(btrim(query)) BETWEEN 1 AND 500),
    status TEXT NOT NULL CHECK (status IN ('completed','failed')),
    raw_response JSONB NOT NULL CHECK (jsonb_typeof(raw_response) = 'object'),
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX location_listing_searches_project_idx ON location_listing_searches(project_id, created_at DESC);

-- Evidence is append-only through the API. The trigger blocks UPDATE only so
-- project deletion can still cascade; no handler issues UPDATE or DELETE.
CREATE FUNCTION protect_location_listing_search_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'location listing search evidence is immutable';
END;
$$;
CREATE TRIGGER location_listing_search_evidence_immutable BEFORE UPDATE ON location_listing_searches
    FOR EACH ROW EXECUTE FUNCTION protect_location_listing_search_evidence();

-- Default grid radius for bound locations. Existing rows keep 5000, the same
-- default the run endpoint already applies.
ALTER TABLE project_locations ADD COLUMN radius_m INTEGER NOT NULL DEFAULT 5000 CHECK (radius_m BETWEEN 1000 AND 25000);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM location_listing_searches) THEN
        RAISE EXCEPTION 'location listing search evidence must be preserved';
    END IF;
END $$;
ALTER TABLE project_locations DROP COLUMN radius_m;
DROP TRIGGER location_listing_search_evidence_immutable ON location_listing_searches;
DROP FUNCTION protect_location_listing_search_evidence();
DROP TABLE location_listing_searches;
-- +goose StatementEnd
