-- CMS approvals are now decided while the worker waits in the same process:
-- the turn keeps its lease, its in-memory transcript, and its SSE connection,
-- so there is no reconstruction step left to feed. The checkpoint table from
-- migration 85 has no reader or writer; ai_cms_approvals stays as the decision
-- record the worker polls.

-- +goose Up
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_turn_checkpoints;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS ai_turn_checkpoints (
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