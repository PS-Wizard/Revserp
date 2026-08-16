-- Snapshot of the workspace AI-tool denylist at turn creation.
--
-- The chat worker reads this column and filters the snapshot out of the tool
-- set it sends to the provider, so a workspace feature change made while a
-- turn is queued never mutates an in-flight turn's tool behavior.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_turns
    ADD COLUMN disabled_ai_tools TEXT[] NOT NULL DEFAULT '{}'::text[];
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_turns
    DROP COLUMN IF EXISTS disabled_ai_tools;
-- +goose StatementEnd
