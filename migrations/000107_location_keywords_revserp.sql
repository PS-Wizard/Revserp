-- +goose Up
-- +goose StatementBegin
ALTER TABLE location_keywords DROP CONSTRAINT IF EXISTS location_keywords_source_check;
ALTER TABLE location_keywords ADD CONSTRAINT location_keywords_source_check
    CHECK (source IN ('user', 'selected', 'revserp'));

CREATE FUNCTION copy_parent_user_keywords_to_location() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER location_keywords_copy_on_location_insert
    AFTER INSERT ON project_locations
    FOR EACH ROW EXECUTE FUNCTION copy_parent_user_keywords_to_location();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM location_keywords WHERE source NOT IN ('user', 'selected')) THEN
        RAISE EXCEPTION 'cannot roll back 000107: location_keywords still holds revserp rows; migrate or delete them first';
    END IF;
END $$;
DROP TRIGGER IF EXISTS location_keywords_copy_on_location_insert ON project_locations;
DROP FUNCTION IF EXISTS copy_parent_user_keywords_to_location();
ALTER TABLE location_keywords DROP CONSTRAINT IF EXISTS location_keywords_source_check;
ALTER TABLE location_keywords ADD CONSTRAINT location_keywords_source_check
    CHECK (source IN ('user', 'selected'));
-- +goose StatementEnd
