-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'assistant_round', 'tool_call', 'tool_result', 'approval_required', 'approval_decided', 'waiting_for_user', 'question_required', 'question_resolved', 'completed', 'stopped', 'failed'));

CREATE TABLE ai_turn_transcripts (
    turn_id UUID PRIMARY KEY REFERENCES ai_turns(id) ON DELETE CASCADE,
    transcript JSONB NOT NULL DEFAULT '[]'::jsonb,
    budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_turn_transcripts;

DELETE FROM ai_turn_events WHERE event_type = 'assistant_round';

ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'tool_call', 'tool_result', 'approval_required', 'approval_decided', 'waiting_for_user', 'question_required', 'question_resolved', 'completed', 'stopped', 'failed'));
-- +goose StatementEnd
