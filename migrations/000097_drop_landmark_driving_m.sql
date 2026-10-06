-- +goose Up
-- +goose StatementBegin
-- Places-only landmarks: Google Places Nearby Search (searchNearby) has no road
-- distance, so the Overpass/OSRM driving_m column is dropped with its range
-- check. Nearby Pro choice: explicit refresh sends exactly one searchNearby
-- call on the key's independent monthly allowance; usage beyond the allowance
-- is billable, and zero app credits must never be read as guarantee-free.
ALTER TABLE location_landmarks DROP COLUMN driving_m;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Original road distances cannot be recovered without the pre-migration backup.
DO $$
BEGIN
    RAISE EXCEPTION 'migration 097 cannot restore road distances; restore the pre-migration backup';
END;
$$;
-- +goose StatementEnd
