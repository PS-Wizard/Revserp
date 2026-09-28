-- +goose Up
-- +goose StatementBegin
-- Platform-admin override for the per-crawl page worker pool size.
-- Singleton row (id always TRUE): absent means "no override", so the
-- boot-time CRAWL_PAGE_WORKER_COUNT env value wins. New crawls read it
-- on claim; already-running crawls keep their pool. No boot reset: the
-- row survives restarts and is shared by every API/worker process via
-- the database.
CREATE TABLE crawl_page_worker_config (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE,
    worker_count INTEGER NOT NULL,
    updated_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crawl_page_worker_config_id_singleton CHECK (id),
    CONSTRAINT crawl_page_worker_config_worker_count_range CHECK (worker_count BETWEEN 1 AND 100)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS crawl_page_worker_config;
-- +goose StatementEnd
