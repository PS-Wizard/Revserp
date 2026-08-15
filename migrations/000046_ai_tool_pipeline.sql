-- AI tool-call pipeline foundations (Phase 8, step 1).
--
-- Everything in this migration is inert: the table, turn status, event types,
-- and gating column are only written by the future agent loop. Nothing in the
-- worker, API, or chat paths consumes them yet.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE ai_tool_calls (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    call_id TEXT NOT NULL,
    name TEXT NOT NULL,
    args JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'awaiting')),
    result_content TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (turn_id, seq),
    UNIQUE (turn_id, call_id)
);

ALTER TABLE ai_turns
    DROP CONSTRAINT IF EXISTS ai_turns_status_check,
    ADD CONSTRAINT ai_turns_status_check
        CHECK (status IN ('queued', 'running', 'waiting', 'completed', 'stopped', 'failed'));

DROP INDEX IF EXISTS idx_ai_turns_one_active_per_conversation;
CREATE UNIQUE INDEX idx_ai_turns_one_active_per_conversation
    ON ai_turns(conversation_id) WHERE status IN ('queued', 'running', 'waiting');

ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'tool_call', 'tool_result', 'completed', 'stopped', 'failed'));

ALTER TABLE organization_features
    ADD COLUMN disabled_ai_tools TEXT[] NOT NULL DEFAULT '{}'::text[];
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE organization_features
    DROP COLUMN IF EXISTS disabled_ai_tools;

ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'completed', 'stopped', 'failed'));

DROP INDEX IF EXISTS idx_ai_turns_one_active_per_conversation;
CREATE UNIQUE INDEX idx_ai_turns_one_active_per_conversation
    ON ai_turns(conversation_id) WHERE status IN ('queued', 'running');

ALTER TABLE ai_turns
    DROP CONSTRAINT IF EXISTS ai_turns_status_check,
    ADD CONSTRAINT ai_turns_status_check
        CHECK (status IN ('queued', 'running', 'completed', 'stopped', 'failed'));

DROP TABLE IF EXISTS ai_tool_calls;
-- +goose StatementEnd
