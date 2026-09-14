-- +goose Up
-- +goose StatementBegin

-- Public Google Maps reviews, keyed globally by place_id: the same listing can
-- appear across projects and competitor sets, so one fetch serves all of them.
-- No TTL column: a refetch is gated by MAPS_VISIBILITY_COOLDOWN, the same knob
-- that gates re-testing the map pack.
CREATE TABLE maps_listing_reviews (
    place_id TEXT PRIMARY KEY,
    cid TEXT,
    reviews JSONB NOT NULL,
    next_page_token TEXT,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    credits_used INTEGER
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS maps_listing_reviews;

-- +goose StatementEnd
