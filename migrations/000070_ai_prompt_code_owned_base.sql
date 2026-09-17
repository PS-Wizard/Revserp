-- +goose Up
-- +goose StatementBegin
-- The chat system prompt is now code-owned. The built-in base prompt always
-- applies, and the two audience columns are optional deltas appended to it
-- instead of full replacements.
--
-- Both columns held a complete copy of the prompt as it existed when an admin
-- last saved the AI config. Those copies are stale: they predate later tools and
-- later profile fields. Clearing them removes the duplication so the code-owned
-- base is the single source of truth, and it lets a future tool or field ship
-- without an admin edit.
--
-- question_generation_prompt is a separate prompt and is left untouched.
UPDATE ai_prompt_configs
SET internal_system_prompt = '',
    external_system_prompt = '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Nothing to restore. The cleared values were stale full copies of a default
-- prompt that the code now owns, so re-populating them would reintroduce the
-- drift this migration removes.
SELECT 1;
-- +goose StatementEnd
