-- +goose Up
-- +goose StatementBegin
CREATE TABLE ai_conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_by_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ai_conversations_project_updated
ON ai_conversations(project_id, updated_at DESC);

CREATE TABLE ai_turns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
    created_by_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'stopped', 'failed')),
    requested_effort TEXT NOT NULL CHECK (requested_effort IN ('none', 'low', 'high', 'max')),
    effective_effort TEXT NOT NULL CHECK (effective_effort IN ('none', 'low', 'high', 'max')),
    model TEXT NOT NULL CHECK (length(btrim(model)) > 0),
    prompt_version TEXT NOT NULL CHECK (length(btrim(prompt_version)) > 0),
    crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    client_request_id TEXT NOT NULL CHECK (length(btrim(client_request_id)) > 0),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    claimed_by TEXT,
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    output_started_at TIMESTAMPTZ,
    prompt_tokens INTEGER CHECK (prompt_tokens IS NULL OR prompt_tokens >= 0),
    reasoning_tokens INTEGER CHECK (reasoning_tokens IS NULL OR reasoning_tokens >= 0),
    completion_tokens INTEGER CHECK (completion_tokens IS NULL OR completion_tokens >= 0),
    total_tokens INTEGER CHECK (total_tokens IS NULL OR total_tokens >= 0),
    error_code TEXT,
    error_message TEXT,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, created_by_user_id, client_request_id)
);
CREATE UNIQUE INDEX idx_ai_turns_one_active_per_conversation
ON ai_turns(conversation_id) WHERE status IN ('queued', 'running');
CREATE INDEX idx_ai_turns_conversation_created
ON ai_turns(conversation_id, created_at);
CREATE INDEX idx_ai_turns_queued
ON ai_turns(queued_at) WHERE status = 'queued';
CREATE INDEX idx_ai_turns_running_lease
ON ai_turns(lease_expires_at) WHERE status = 'running';
CREATE INDEX idx_ai_turns_creator_active
ON ai_turns(created_by_user_id, created_at) WHERE status IN ('queued', 'running');

CREATE TABLE ai_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'complete', 'partial', 'failed')),
    content TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (turn_id, role),
    CHECK (
        (role = 'user' AND status = 'complete')
        OR (role = 'assistant' AND status IN ('pending', 'complete', 'partial', 'failed'))
    )
);

CREATE TABLE ai_turn_events (
    id BIGSERIAL PRIMARY KEY,
    turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('phase', 'text_delta', 'completed', 'stopped', 'failed')),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ai_turn_events_turn_id_id
ON ai_turn_events(turn_id, id);

CREATE TABLE ai_workspace_monthly_usage (
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    period_start DATE NOT NULL CHECK (EXTRACT(DAY FROM period_start) = 1),
    used_messages INTEGER NOT NULL DEFAULT 0 CHECK (used_messages >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, period_start)
);

ALTER TABLE organization_features
    ADD COLUMN ai_monthly_message_limit INTEGER NOT NULL DEFAULT 50
        CHECK (ai_monthly_message_limit >= 0),
    ADD COLUMN ai_max_reasoning_effort TEXT NOT NULL DEFAULT 'high'
        CHECK (ai_max_reasoning_effort IN ('none', 'low', 'high', 'max'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE organization_features
    DROP COLUMN IF EXISTS ai_max_reasoning_effort,
    DROP COLUMN IF EXISTS ai_monthly_message_limit;
DROP TABLE IF EXISTS ai_turn_events;
DROP TABLE IF EXISTS ai_messages;
DROP TABLE IF EXISTS ai_turns;
DROP TABLE IF EXISTS ai_workspace_monthly_usage;
DROP TABLE IF EXISTS ai_conversations;
-- +goose StatementEnd
