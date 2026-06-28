-- +goose Up
-- +goose StatementBegin

CREATE INDEX IF NOT EXISTS idx_crawls_project_id_created_at ON crawls (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_crawl_issues_crawl_pillar_bucket_type ON crawl_issues (crawl_id, pillar, bucket, issue_type);
CREATE INDEX IF NOT EXISTS idx_crawl_links_crawl_id_is_internal ON crawl_links (crawl_id, is_internal);
CREATE INDEX IF NOT EXISTS idx_ai_audits_project_created ON ai_audits (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_crawl_pages_crawl_id_created_at ON crawl_pages (crawl_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_crawl_issues_crawl_id_created_at ON crawl_issues (crawl_id, created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_crawls_project_id_created_at;
DROP INDEX IF EXISTS idx_crawl_issues_crawl_pillar_bucket_type;
DROP INDEX IF EXISTS idx_crawl_links_crawl_id_is_internal;
DROP INDEX IF EXISTS idx_ai_audits_project_created;
DROP INDEX IF EXISTS idx_crawl_pages_crawl_id_created_at;
DROP INDEX IF EXISTS idx_crawl_issues_crawl_id_created_at;

-- +goose StatementEnd
