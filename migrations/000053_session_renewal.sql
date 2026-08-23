-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions
ADD COLUMN previous_session_token_hash TEXT,
ADD COLUMN previous_session_token_expires_at TIMESTAMPTZ,
ADD COLUMN supabase_refresh_retry_after TIMESTAMPTZ,
ADD COLUMN supabase_refresh_disabled_at TIMESTAMPTZ;

CREATE INDEX idx_sessions_previous_token_hash
ON sessions(previous_session_token_hash)
WHERE previous_session_token_hash IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sessions_previous_token_hash;

ALTER TABLE sessions
DROP COLUMN IF EXISTS supabase_refresh_disabled_at,
DROP COLUMN IF EXISTS supabase_refresh_retry_after,
DROP COLUMN IF EXISTS previous_session_token_expires_at,
DROP COLUMN IF EXISTS previous_session_token_hash;
-- +goose StatementEnd
