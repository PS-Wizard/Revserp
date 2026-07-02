-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawls ADD COLUMN phase TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawls DROP COLUMN phase;
-- +goose StatementEnd
