-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_messages
    ADD COLUMN reasoning_content TEXT,
    ADD COLUMN tool_calls JSONB,
    ADD COLUMN tool_call_id TEXT,
    ADD COLUMN tool_name TEXT;

ALTER TABLE ai_messages DROP CONSTRAINT IF EXISTS ai_messages_role_check;
ALTER TABLE ai_messages ADD CONSTRAINT ai_messages_role_check CHECK (role IN ('user', 'assistant', 'tool'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_messages DROP CONSTRAINT IF EXISTS ai_messages_role_check;
ALTER TABLE ai_messages ADD CONSTRAINT ai_messages_role_check CHECK (role IN ('user', 'assistant'));

ALTER TABLE ai_messages
    DROP COLUMN IF EXISTS reasoning_content,
    DROP COLUMN IF EXISTS tool_calls,
    DROP COLUMN IF EXISTS tool_call_id,
    DROP COLUMN IF EXISTS tool_name;
-- +goose StatementEnd
