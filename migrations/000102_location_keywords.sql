-- +goose Up
-- +goose StatementBegin
-- Location keyword lists are independent from parent project keywords.
-- Only explicit user and selected rows are stored, both start empty.
-- Revserp suggestions stay derived from saved services, localities and
-- landmarks, so they need no table here.
CREATE TABLE location_keywords (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL REFERENCES project_locations(id) ON DELETE CASCADE,
    keyword TEXT NOT NULL CHECK (length(btrim(keyword)) > 0),
    normalized_keyword TEXT NOT NULL CHECK (length(btrim(normalized_keyword)) > 0),
    kind TEXT NOT NULL CHECK (kind IN ('brand', 'non_brand')),
    source TEXT NOT NULL CHECK (source IN ('user', 'selected')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (location_id, source, normalized_keyword)
);
CREATE INDEX location_keywords_location_id_idx ON location_keywords(location_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS location_keywords;
-- +goose StatementEnd
