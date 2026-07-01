-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_audits ADD CONSTRAINT ai_audits_project_crawl_unique UNIQUE (project_id, crawl_id);
ALTER TABLE ai_worker_jobs ADD COLUMN audit_id UUID REFERENCES ai_audits(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_worker_jobs DROP COLUMN IF EXISTS audit_id;
ALTER TABLE ai_audits DROP CONSTRAINT IF EXISTS ai_audits_project_crawl_unique;
-- +goose StatementEnd
