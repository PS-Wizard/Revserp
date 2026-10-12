
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'tool_call', 'tool_result', 'approval_required', 'approval_decided', 'waiting_for_user', 'question_required', 'question_resolved', 'completed', 'stopped', 'failed'));

CREATE TABLE ai_user_question_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL CHECK (length(btrim(tool_call_id)) > 0),
    questions JSONB NOT NULL DEFAULT '[]'::jsonb,
    answers JSONB,
    reply TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'answered', 'skipped', 'cancelled')),
    decided_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (turn_id, tool_call_id)
);
CREATE UNIQUE INDEX idx_ai_user_question_one_pending_per_turn
    ON ai_user_question_requests(turn_id) WHERE status = 'pending';
CREATE INDEX idx_ai_user_question_turn_created
    ON ai_user_question_requests(turn_id, created_at);

CREATE TABLE ai_user_question_checkpoints (
    turn_id UUID PRIMARY KEY REFERENCES ai_turns(id) ON DELETE CASCADE,
    question_request_id UUID NOT NULL REFERENCES ai_user_question_requests(id) ON DELETE CASCADE,
    messages JSONB NOT NULL,
    usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
    rundown JSONB NOT NULL DEFAULT '{}'::jsonb,
    deferred_calls JSONB NOT NULL DEFAULT '[]'::jsonb,
    round INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
    next_seq INTEGER NOT NULL DEFAULT 0 CHECK (next_seq >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_user_question_checkpoints;
DROP TABLE IF EXISTS ai_user_question_requests;

ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'tool_call', 'tool_result', 'approval_required', 'approval_decided', 'waiting_for_user', 'completed', 'stopped', 'failed'));
-- +goose StatementEnd
