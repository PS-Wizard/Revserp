-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_prompt_configs ADD COLUMN question_generation_prompt TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_prompt_configs DROP COLUMN IF EXISTS question_generation_prompt;
-- +goose StatementEnd
