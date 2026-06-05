-- +goose Up
-- +goose StatementBegin
CREATE TABLE crawl_score_breakdowns (
    crawl_id UUID PRIMARY KEY REFERENCES crawls(id) ON DELETE CASCADE,
    scoring_version TEXT NOT NULL,
    breakdown_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS crawl_score_breakdowns;
-- +goose StatementEnd
