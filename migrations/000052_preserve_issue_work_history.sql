-- +goose Up
-- +goose StatementBegin
ALTER TABLE issue_work_attempts
    DROP CONSTRAINT issue_work_attempts_source_crawl_id_fkey,
    ALTER COLUMN source_crawl_id DROP NOT NULL,
    ADD CONSTRAINT issue_work_attempts_source_crawl_id_fkey
        FOREIGN KEY (source_crawl_id) REFERENCES crawls(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM issue_work_attempts WHERE source_crawl_id IS NULL;
ALTER TABLE issue_work_attempts
    DROP CONSTRAINT issue_work_attempts_source_crawl_id_fkey,
    ALTER COLUMN source_crawl_id SET NOT NULL,
    ADD CONSTRAINT issue_work_attempts_source_crawl_id_fkey
        FOREIGN KEY (source_crawl_id) REFERENCES crawls(id) ON DELETE CASCADE;
-- +goose StatementEnd
