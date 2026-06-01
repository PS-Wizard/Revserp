-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawls
ADD COLUMN requested_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX idx_crawls_status_created_at ON crawls(status, created_at);

CREATE UNIQUE INDEX idx_crawls_one_running_per_user
ON crawls(requested_by_user_id)
WHERE status = 'running'
  AND requested_by_user_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_crawls_one_running_per_user;
DROP INDEX IF EXISTS idx_crawls_status_created_at;
ALTER TABLE crawls
DROP COLUMN IF EXISTS requested_by_user_id;
-- +goose StatementEnd
