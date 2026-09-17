-- +goose Up
-- +goose StatementBegin

CREATE TABLE competitor_gap_reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    competitor_crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    version TEXT NOT NULL,
    report_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (competitor_crawl_id)
);
CREATE INDEX idx_competitor_gap_reports_parent ON competitor_gap_reports(parent_crawl_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_competitor_gap_reports_parent;
DROP TABLE IF EXISTS competitor_gap_reports;

-- +goose StatementEnd
