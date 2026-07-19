-- +goose Up
-- +goose StatementBegin
-- Conversations are now org-scoped: they belong to the user's active
-- organization, not a specific project/crawl. The active project/crawl are
-- contextual, injected per message, and no longer stored on the conversation.
--
-- Written idempotently (IF NOT EXISTS / no-op-safe ALTERs) so it can be safely
-- re-applied against a database whose schema was already partially migrated by
-- hand or by a since-reverted branch.
ALTER TABLE ai_conversations
    ADD COLUMN IF NOT EXISTS org_id UUID REFERENCES organizations(id) ON DELETE CASCADE;

UPDATE ai_conversations AS ac
SET org_id = p.organization_id
FROM projects AS p
WHERE p.id = ac.project_id
  AND ac.org_id IS NULL;

ALTER TABLE ai_conversations ALTER COLUMN org_id SET NOT NULL;

-- project_id/crawl_id are no longer required; a conversation may exist with no
-- active project/crawl context at creation time.
ALTER TABLE ai_conversations ALTER COLUMN project_id DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ai_conversations_org_updated
ON ai_conversations(org_id, updated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_ai_conversations_org_updated;

UPDATE ai_conversations SET project_id = (
    SELECT p.id FROM projects AS p WHERE p.organization_id = ai_conversations.org_id LIMIT 1
)
WHERE project_id IS NULL;

ALTER TABLE ai_conversations ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE ai_conversations DROP COLUMN IF EXISTS org_id;
-- +goose StatementEnd
