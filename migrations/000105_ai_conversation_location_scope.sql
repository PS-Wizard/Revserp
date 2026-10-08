-- +goose Up
-- +goose StatementBegin
-- Durable assistant scope. A conversation belongs either to the parent project
-- (location_id NULL) or to exactly one location of that project, so parent and
-- location chats are created and listed separately and their worker history
-- never mixes. The composite FK keeps project and location consistent; the
-- default NO ACTION means a location with conversations cannot be deleted until
-- those conversations are removed, and historical chat is never nulled out.
ALTER TABLE ai_conversations ADD COLUMN location_id UUID;
ALTER TABLE ai_conversations
    ADD CONSTRAINT ai_conversations_location_project_fkey
    FOREIGN KEY (location_id, project_id)
    REFERENCES project_locations(id, project_id) ON DELETE NO ACTION;
CREATE INDEX ai_conversations_project_location_updated_idx
    ON ai_conversations(project_id, location_id, updated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS ai_conversations_project_location_updated_idx;
ALTER TABLE ai_conversations DROP CONSTRAINT IF EXISTS ai_conversations_location_project_fkey;
ALTER TABLE ai_conversations DROP COLUMN IF EXISTS location_id;
-- +goose StatementEnd
