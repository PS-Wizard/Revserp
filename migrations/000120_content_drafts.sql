-- +goose Up
-- +goose StatementBegin
-- Content drafts: one markdown blog draft per row, each with exactly one
-- attached Revbot conversation. No revisions, no images, no status column;
-- the current body is the draft. The attached conversation is scoped out of
-- the normal conversation list via content_draft_id IS NULL filters.
CREATE TABLE content_drafts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_by_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    meta_title TEXT NOT NULL DEFAULT '',
    meta_description TEXT NOT NULL DEFAULT '',
    target_keyword TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_content_drafts_project_updated ON content_drafts(project_id, updated_at DESC);
ALTER TABLE ai_conversations ADD COLUMN content_draft_id UUID REFERENCES content_drafts(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX idx_ai_conversations_content_draft ON ai_conversations(content_draft_id) WHERE content_draft_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_ai_conversations_content_draft;
ALTER TABLE ai_conversations DROP COLUMN IF EXISTS content_draft_id;
DROP INDEX IF EXISTS idx_content_drafts_project_updated;
DROP TABLE IF EXISTS content_drafts;
-- +goose StatementEnd
