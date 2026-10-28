-- +goose Up
-- +goose StatementBegin

-- Opt-in crawl finish emails. notify_email/origin are stamped at start-crawl
-- time from the session user and request origin (never client-sent); the
-- worker sends one email when the crawl reaches completed/failed and then
-- sets notified_at exactly once so a retry can never double-send.
ALTER TABLE crawls
    ADD COLUMN notify_email TEXT,
    ADD COLUMN notify_origin TEXT,
    ADD COLUMN notified_at TIMESTAMPTZ;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawls
    DROP COLUMN IF EXISTS notified_at,
    DROP COLUMN IF EXISTS notify_origin,
    DROP COLUMN IF EXISTS notify_email;
-- +goose StatementEnd
