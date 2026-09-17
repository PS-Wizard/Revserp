-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_messages ADD COLUMN content_blocks JSONB;
ALTER TABLE ai_messages ADD CONSTRAINT chk_ai_messages_content_blocks_array CHECK (content_blocks IS NULL OR jsonb_typeof(content_blocks) = 'array');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_messages DROP CONSTRAINT IF EXISTS chk_ai_messages_content_blocks_array;
ALTER TABLE ai_messages DROP COLUMN IF EXISTS content_blocks;
-- +goose StatementEnd
