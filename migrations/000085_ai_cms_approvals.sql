-- Durable CMS tool-call approvals: a waiting_for_user turn status, the
-- approval and resume-checkpoint tables, and the SSE event types the approval
-- flow persists. Builds on migration 46 (which owns the waiting/awaiting
-- states and the event-type CHECK); migration 42 is superseded for these
-- constraints. Legacy 'waiting' stays allowed wherever it already was.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_turns
    DROP CONSTRAINT IF EXISTS ai_turns_status_check,
    ADD CONSTRAINT ai_turns_status_check
        CHECK (status IN ('queued', 'running', 'waiting', 'waiting_for_user', 'completed', 'stopped', 'failed'));

DROP INDEX IF EXISTS idx_ai_turns_one_active_per_conversation;
CREATE UNIQUE INDEX idx_ai_turns_one_active_per_conversation
    ON ai_turns(conversation_id) WHERE status IN ('queued', 'running', 'waiting', 'waiting_for_user');

DROP INDEX IF EXISTS idx_ai_turns_creator_active;
CREATE INDEX idx_ai_turns_creator_active
    ON ai_turns(created_by_user_id, created_at) WHERE status IN ('queued', 'running', 'waiting', 'waiting_for_user');

ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'tool_call', 'tool_result', 'approval_required', 'approval_decided', 'waiting_for_user', 'completed', 'stopped', 'failed'));

-- One row per paused CMS tool call. proposed_args is the immutable original
-- call: decisions never carry replacement args. connection_revision pins the
-- CMS connection the approval was prepared against.
CREATE TABLE ai_cms_approvals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL CHECK (length(btrim(tool_call_id)) > 0),
    tool_name TEXT NOT NULL CHECK (length(btrim(tool_name)) > 0),
    provider TEXT NOT NULL CHECK (provider IN ('rune', 'wordpress')),
    target TEXT NOT NULL DEFAULT '',
    before_text TEXT NOT NULL DEFAULT '',
    after_text TEXT NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    proposed_args JSONB NOT NULL DEFAULT '{}'::jsonb,
    connection_revision UUID,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'invalidated', 'executing', 'completed', 'failed')),
    decided_at TIMESTAMPTZ,
    decided_by UUID REFERENCES users(id) ON DELETE SET NULL,
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (turn_id, tool_call_id)
);
CREATE INDEX idx_ai_cms_approvals_turn_created
    ON ai_cms_approvals(turn_id, created_at);

-- Bounded durable checkpoint for a paused turn. ai_messages allows only one
-- row per (turn_id, role), so the exact in-turn provider transcript
-- (assistant messages with tool calls plus completed tool results) lives
-- here, not in ai_messages. Base history is rebuilt from completed turns on
-- resume, never duplicated from this row.
CREATE TABLE ai_turn_checkpoints (
    turn_id UUID PRIMARY KEY REFERENCES ai_turns(id) ON DELETE CASCADE,
    messages JSONB NOT NULL,
    remaining_calls JSONB NOT NULL DEFAULT '[]'::jsonb,
    usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
    round INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_turn_checkpoints;
DROP TABLE IF EXISTS ai_cms_approvals;

ALTER TABLE ai_turn_events
    DROP CONSTRAINT IF EXISTS ai_turn_events_event_type_check,
    ADD CONSTRAINT ai_turn_events_event_type_check
        CHECK (event_type IN ('phase', 'text_delta', 'tool_call', 'tool_result', 'completed', 'stopped', 'failed'));

DROP INDEX IF EXISTS idx_ai_turns_creator_active;
CREATE INDEX idx_ai_turns_creator_active
    ON ai_turns(created_by_user_id, created_at) WHERE status IN ('queued', 'running');

DROP INDEX IF EXISTS idx_ai_turns_one_active_per_conversation;
CREATE UNIQUE INDEX idx_ai_turns_one_active_per_conversation
    ON ai_turns(conversation_id) WHERE status IN ('queued', 'running', 'waiting');

ALTER TABLE ai_turns
    DROP CONSTRAINT IF EXISTS ai_turns_status_check,
    ADD CONSTRAINT ai_turns_status_check
        CHECK (status IN ('queued', 'running', 'waiting', 'completed', 'stopped', 'failed'));
-- +goose StatementEnd
