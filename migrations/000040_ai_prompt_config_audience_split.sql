-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_prompt_configs
    ADD COLUMN internal_system_prompt TEXT NOT NULL DEFAULT '',
    ADD COLUMN external_system_prompt TEXT NOT NULL DEFAULT '';

UPDATE ai_prompt_configs
SET
    internal_system_prompt = context_prompt ||
        CASE WHEN guidelines_prompt <> '' THEN E'\n' || guidelines_prompt ELSE '' END ||
        CASE WHEN other_notes_prompt <> '' THEN E'\nAdditional notes:\n' || other_notes_prompt ELSE '' END,
    external_system_prompt = context_prompt ||
        CASE WHEN guidelines_prompt <> '' THEN E'\n' || guidelines_prompt ELSE '' END ||
        CASE WHEN other_notes_prompt <> '' THEN E'\nAdditional notes:\n' || other_notes_prompt ELSE '' END;

ALTER TABLE ai_prompt_configs
    DROP COLUMN context_prompt,
    DROP COLUMN guidelines_prompt,
    DROP COLUMN other_notes_prompt;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_prompt_configs
    ADD COLUMN context_prompt TEXT NOT NULL DEFAULT '',
    ADD COLUMN guidelines_prompt TEXT NOT NULL DEFAULT '',
    ADD COLUMN other_notes_prompt TEXT NOT NULL DEFAULT '';

UPDATE ai_prompt_configs
SET context_prompt = external_system_prompt;

ALTER TABLE ai_prompt_configs
    DROP COLUMN internal_system_prompt,
    DROP COLUMN external_system_prompt;
-- +goose StatementEnd
