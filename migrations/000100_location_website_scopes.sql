-- +goose Up
-- +goose StatementBegin
-- Revisioned branch website scopes. The latest revision per location is
-- current. match='none' (url NULL) means no branch audit, which the API
-- renders as an empty/info state rather than parent scores.
CREATE TABLE location_website_scopes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    location_id UUID NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    url TEXT,
    match TEXT NOT NULL CHECK (match IN ('none', 'exact', 'subtree')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (location_id, revision),
    FOREIGN KEY (location_id, project_id)
        REFERENCES project_locations(id, project_id) ON DELETE CASCADE,
    CHECK ((match = 'none') = (url IS NULL))
);
CREATE INDEX location_website_scopes_location_idx
    ON location_website_scopes(location_id, revision DESC);

-- Revisions are append-only history; graphs identify scope changes instead
-- of silently mixing them. UPDATE always rejects. DELETE rejects while the
-- owning location still exists, but permits cleanup once the parent
-- location is already gone: that is the declared FK cascade from
-- project_locations (and through it, projects and organizations) doing
-- its delete, not a direct revision rewrite. Clearing a scope in the API
-- appends a none revision; it never deletes rows.
CREATE FUNCTION protect_location_website_scope_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'location website scope revisions are immutable';
    END IF;
    IF EXISTS (SELECT 1 FROM project_locations WHERE id = OLD.location_id) THEN
        RAISE EXCEPTION 'location website scope revisions are immutable';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER location_website_scope_revision_immutable
    BEFORE UPDATE OR DELETE ON location_website_scopes
    FOR EACH ROW EXECUTE FUNCTION protect_location_website_scope_revision();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS location_website_scope_revision_immutable ON location_website_scopes;
DROP FUNCTION IF EXISTS protect_location_website_scope_revision();
DROP TABLE IF EXISTS location_website_scopes;
-- +goose StatementEnd
