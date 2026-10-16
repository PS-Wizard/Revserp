-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION copy_parent_user_keywords_to_location() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source)
    SELECT NEW.id, keyword, normalized_keyword, kind, 'user'
    FROM project_keywords WHERE project_id = NEW.project_id AND source = 'user'
    ON CONFLICT (location_id, source, normalized_keyword) DO NOTHING;
    INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source)
    SELECT NEW.id, keyword, normalized_keyword, kind, 'selected'
    FROM project_keywords WHERE project_id = NEW.project_id AND source = 'user'
    ON CONFLICT (location_id, source, normalized_keyword) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION copy_parent_user_keywords_to_location() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source)
    SELECT NEW.id, keyword, normalized_keyword, kind, 'user'
    FROM project_keywords WHERE project_id = NEW.project_id AND source = 'user'
    ON CONFLICT (location_id, source, normalized_keyword) DO NOTHING;
    INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source)
    SELECT NEW.id, keyword, normalized_keyword, kind, 'selected'
    FROM project_keywords WHERE project_id = NEW.project_id AND source = 'user'
    ON CONFLICT (location_id, source, normalized_keyword) DO NOTHING;
    INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin, landmark_id)
    SELECT NEW.id, pk.keyword, pk.normalized_keyword,
        ROW_NUMBER() OVER (ORDER BY pk.created_at, pk.id) - 1,
        TRUE, 'map', 'manual', 'service', NULL
    FROM project_keywords pk
    WHERE pk.project_id = NEW.project_id AND pk.source = 'user'
        AND octet_length(pk.keyword) <= 500
        AND btrim(pk.keyword) <> ''
        AND btrim(pk.normalized_keyword) <> ''
    ON CONFLICT (location_id, kind, normalized) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
