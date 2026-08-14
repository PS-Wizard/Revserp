-- +goose Up
-- +goose StatementBegin
-- DESTRUCTIVE: permanently deletes all legacy interactive AI chat conversations,
-- messages, and optional artifacts. This migration cannot restore that data.
DROP TABLE IF EXISTS ai_artifacts;
DROP TABLE IF EXISTS ai_messages;
DROP TABLE IF EXISTS ai_conversations;
ALTER TABLE organization_features DROP COLUMN IF EXISTS disabled_ai_tools;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Schema-only rollback. Deleted chat and artifact data cannot be restored.
CREATE TABLE ai_conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    created_by_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE
);
CREATE INDEX idx_ai_conversations_project_updated
ON ai_conversations(project_id, updated_at DESC);
CREATE INDEX idx_ai_conversations_crawl_updated
ON ai_conversations(crawl_id, updated_at DESC);
CREATE INDEX idx_ai_conversations_org_updated
ON ai_conversations(org_id, updated_at DESC);

CREATE TABLE ai_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    scope JSONB,
    model TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reasoning_content TEXT,
    tool_calls JSONB,
    tool_call_id TEXT,
    tool_name TEXT
);
CREATE INDEX idx_ai_messages_conversation_created
ON ai_messages(conversation_id, created_at ASC);

ALTER TABLE organization_features
    ADD COLUMN IF NOT EXISTS disabled_ai_tools TEXT[] NOT NULL DEFAULT '{}';
-- +goose StatementEnd
