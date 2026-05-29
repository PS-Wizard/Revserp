-- +goose Up
-- +goose StatementBegin
CREATE TABLE crawl_issues (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    crawl_page_id UUID REFERENCES crawl_pages(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    severity TEXT NOT NULL,
    category TEXT NOT NULL,
    code TEXT NOT NULL,
    message TEXT NOT NULL,
    details TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crawl_issues_crawl_id ON crawl_issues(crawl_id);
CREATE INDEX idx_crawl_issues_crawl_page_id ON crawl_issues(crawl_page_id);
CREATE INDEX idx_crawl_issues_crawl_id_severity ON crawl_issues(crawl_id, severity);
CREATE INDEX idx_crawl_issues_crawl_id_category ON crawl_issues(crawl_id, category);
CREATE UNIQUE INDEX idx_crawl_issues_unique_page_code ON crawl_issues(crawl_id, crawl_page_id, code);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS crawl_issues;
-- +goose StatementEnd
