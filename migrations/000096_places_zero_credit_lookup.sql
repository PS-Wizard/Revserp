-- +goose Up
-- +goose StatementBegin
-- Places identity searches within the monthly free tier have a known zero cost.
ALTER TABLE local_listing_lookups DROP CONSTRAINT local_listing_lookups_expected_credits_check;
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookups_expected_credits_check CHECK (expected_credits IN (0,1,3));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM local_listing_lookups WHERE expected_credits = 0) THEN
        RAISE EXCEPTION 'zero-credit listing evidence must be preserved';
    END IF;
END $$;
ALTER TABLE local_listing_lookups DROP CONSTRAINT local_listing_lookups_expected_credits_check;
ALTER TABLE local_listing_lookups ADD CONSTRAINT local_listing_lookups_expected_credits_check CHECK (expected_credits IN (1,3));
-- +goose StatementEnd
