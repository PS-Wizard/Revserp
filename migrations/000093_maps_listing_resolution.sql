-- +goose Up
-- +goose StatementBegin
ALTER TABLE local_listing_lookups DROP CONSTRAINT local_listing_lookups_expected_credits_check;
ALTER TABLE local_listing_lookups DROP CONSTRAINT local_listing_lookups_reserved_credits_check;
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookups_expected_credits_check CHECK (expected_credits IN (1,3));
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookups_reserved_credits_check CHECK (reserved_credits BETWEEN 0 AND expected_credits);
ALTER TABLE local_listing_lookups ALTER COLUMN expected_credits SET DEFAULT 3;
ALTER TABLE local_listing_lookups ALTER COLUMN reserved_credits SET DEFAULT 3;
ALTER TABLE local_listing_lookups ADD COLUMN candidate_key TEXT NOT NULL DEFAULT '';
ALTER TABLE local_listing_lookups ADD COLUMN source_latitude DOUBLE PRECISION CHECK (source_latitude BETWEEN -90 AND 90);
ALTER TABLE local_listing_lookups ADD COLUMN source_longitude DOUBLE PRECISION CHECK (source_longitude BETWEEN -180 AND 180);
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookup_source_pair CHECK ((source_latitude IS NULL) = (source_longitude IS NULL));

CREATE OR REPLACE FUNCTION protect_listing_lookup_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.location_id IS DISTINCT FROM OLD.location_id
       OR NEW.query IS DISTINCT FROM OLD.query
       OR NEW.expected_credits IS DISTINCT FROM OLD.expected_credits
       OR NEW.candidate_key IS DISTINCT FROM OLD.candidate_key
       OR NEW.source_latitude IS DISTINCT FROM OLD.source_latitude
       OR NEW.source_longitude IS DISTINCT FROM OLD.source_longitude
       OR OLD.status <> 'running' THEN
        RAISE EXCEPTION 'listing lookup evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM local_listing_lookups WHERE expected_credits <> 1 OR reserved_credits > 1) THEN
        RAISE EXCEPTION 'three-credit listing evidence must be preserved; cannot remove Maps resolution';
    END IF;
END $$;
CREATE OR REPLACE FUNCTION protect_listing_lookup_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
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
ALTER TABLE local_listing_lookups DROP COLUMN candidate_key, DROP COLUMN source_latitude, DROP COLUMN source_longitude;
ALTER TABLE local_listing_lookups DROP CONSTRAINT local_listing_lookups_expected_credits_check;
ALTER TABLE local_listing_lookups DROP CONSTRAINT local_listing_lookups_reserved_credits_check;
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookups_expected_credits_check CHECK (expected_credits = 1);
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookups_reserved_credits_check CHECK (reserved_credits BETWEEN 0 AND 1);
ALTER TABLE local_listing_lookups ALTER COLUMN expected_credits SET DEFAULT 1;
ALTER TABLE local_listing_lookups ALTER COLUMN reserved_credits SET DEFAULT 1;
-- +goose StatementEnd
