-- +goose Up
-- +goose StatementBegin
ALTER TABLE local_visibility_results ADD CONSTRAINT local_visibility_found_rank_required
    CHECK (match_status <> 'found' OR rank IS NOT NULL);

CREATE FUNCTION protect_location_unsettled_maps_spend() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM local_visibility_runs
        WHERE location_id = OLD.id
          AND (status IN ('queued','running') OR reserved_credits > 0)
    ) THEN
        RAISE EXCEPTION 'location has active or unconfirmed Maps spend; settle before deletion'
            USING ERRCODE = '23503';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER location_unsettled_maps_spend_guard BEFORE DELETE ON project_locations
    FOR EACH ROW EXECUTE FUNCTION protect_location_unsettled_maps_spend();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER location_unsettled_maps_spend_guard ON project_locations;
DROP FUNCTION protect_location_unsettled_maps_spend();
ALTER TABLE local_visibility_results DROP CONSTRAINT local_visibility_found_rank_required;
-- +goose StatementEnd
