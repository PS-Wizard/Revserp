-- +goose Up
-- +goose StatementBegin
-- Audit history is append-only: a rerun after a terminal (completed,
-- completed_with_failures, failed) audit must be allowed. The all-history
-- unique constraint from 000031 blocks that, so replace it with a partial
-- unique index permitting at most one queued/running audit per non-null
-- (project_id, crawl_id), including under concurrent inserts.
ALTER TABLE ai_audits DROP CONSTRAINT IF EXISTS ai_audits_project_crawl_unique;
CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_audits_one_active_per_crawl
ON ai_audits (project_id, crawl_id)
WHERE status IN ('queued', 'running')
  AND crawl_id IS NOT NULL;
-- The dropped constraint was the only composite (project_id, crawl_id)
-- lookup support; keep a non-unique index for latest-by-crawl lookups.
CREATE INDEX IF NOT EXISTS idx_ai_audits_project_crawl_created
ON ai_audits (project_id, crawl_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_ai_audits_project_crawl_created;
DROP INDEX IF EXISTS idx_ai_audits_one_active_per_crawl;
ALTER TABLE ai_audits ADD CONSTRAINT ai_audits_project_crawl_unique UNIQUE (project_id, crawl_id);
-- +goose StatementEnd
