-- +goose Up
-- +goose StatementBegin
-- Enforces one running auto crawl per project. The per-user index
-- idx_crawls_one_running_per_user (000008) cannot guard auto crawls because
-- requested_by_user_id is NULL for them, so two concurrent claimers could both
-- start one project's crawl.
--
-- PRE-CHECK before applying: existing data must not already violate this index.
-- The operator must run and resolve any returned rows first, or this migration
-- fails on creation:
--   SELECT project_id FROM crawls
--   WHERE status = 'running' AND source = 'auto'
--   GROUP BY project_id HAVING count(*) > 1;
CREATE UNIQUE INDEX idx_crawls_one_running_auto_per_project
ON crawls(project_id)
WHERE status = 'running'
  AND source = 'auto';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_crawls_one_running_auto_per_project;
-- +goose StatementEnd
