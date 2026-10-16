-- +goose Up
-- +goose StatementBegin
CREATE TABLE user_announcement_seen (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    announcement_id VARCHAR(100) NOT NULL CHECK (announcement_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$'),
    seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, announcement_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS user_announcement_seen;
-- +goose StatementEnd
