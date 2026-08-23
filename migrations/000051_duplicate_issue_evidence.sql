-- +goose Up
-- +goose StatementBegin
CREATE TABLE crawl_issue_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    issue_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crawl_issue_groups_crawl_id ON crawl_issue_groups(crawl_id);

CREATE TABLE crawl_issue_group_members (
    group_id UUID NOT NULL REFERENCES crawl_issue_groups(id) ON DELETE CASCADE,
    crawl_page_id UUID NOT NULL REFERENCES crawl_pages(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    PRIMARY KEY (group_id, crawl_page_id)
);

CREATE INDEX idx_crawl_issue_group_members_crawl_page_id ON crawl_issue_group_members(crawl_page_id);

CREATE TABLE crawl_issue_relations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    issue_type TEXT NOT NULL,
    left_crawl_page_id UUID NOT NULL REFERENCES crawl_pages(id) ON DELETE CASCADE,
    right_crawl_page_id UUID NOT NULL REFERENCES crawl_pages(id) ON DELETE CASCADE,
    similarity DOUBLE PRECISION,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_crawl_issue_relations_crawl_id ON crawl_issue_relations(crawl_id);
CREATE INDEX idx_crawl_issue_relations_left_crawl_page_id ON crawl_issue_relations(left_crawl_page_id);
CREATE INDEX idx_crawl_issue_relations_right_crawl_page_id ON crawl_issue_relations(right_crawl_page_id);
CREATE UNIQUE INDEX idx_crawl_issue_relations_unique_pair ON crawl_issue_relations(crawl_id, issue_type, left_crawl_page_id, right_crawl_page_id);

ALTER TABLE crawl_issues ADD COLUMN issue_group_id UUID REFERENCES crawl_issue_groups(id) ON DELETE SET NULL;

CREATE INDEX idx_crawl_issues_issue_group_id ON crawl_issues(issue_group_id);

ALTER TABLE issue_work_items ADD COLUMN source_issue_group_id UUID REFERENCES crawl_issue_groups(id) ON DELETE SET NULL;
CREATE INDEX idx_issue_work_items_source_issue_group_id ON issue_work_items(source_issue_group_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE issue_work_items DROP COLUMN IF EXISTS source_issue_group_id;
ALTER TABLE crawl_issues DROP COLUMN IF EXISTS issue_group_id;
DROP TABLE IF EXISTS crawl_issue_relations;
DROP TABLE IF EXISTS crawl_issue_group_members;
DROP TABLE IF EXISTS crawl_issue_groups;
-- +goose StatementEnd
