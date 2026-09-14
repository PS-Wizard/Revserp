-- +goose Up
-- +goose StatementBegin

ALTER TABLE organization_features
    ADD COLUMN max_competitors INTEGER NOT NULL DEFAULT 3;

ALTER TABLE organization_features
    ADD CONSTRAINT chk_organization_features_max_competitors CHECK (max_competitors >= 0);

CREATE TABLE project_competitors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    seed_url TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, seed_url)
);

CREATE INDEX idx_project_competitors_project_id ON project_competitors(project_id);

ALTER TABLE crawls
    ADD COLUMN competitor_id UUID REFERENCES project_competitors(id) ON DELETE CASCADE,
    ADD COLUMN parent_crawl_id UUID REFERENCES crawls(id) ON DELETE CASCADE;

ALTER TABLE crawls DROP CONSTRAINT IF EXISTS crawls_source_check;

ALTER TABLE crawls
    ADD CONSTRAINT crawls_source_check CHECK (source IN ('manual', 'auto', 'competitor'));

CREATE UNIQUE INDEX idx_crawls_parent_competitor
    ON crawls(parent_crawl_id, competitor_id)
    WHERE parent_crawl_id IS NOT NULL AND competitor_id IS NOT NULL;

CREATE INDEX idx_crawls_parent_crawl_id ON crawls(parent_crawl_id)
    WHERE parent_crawl_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_crawls_parent_crawl_id;
DROP INDEX IF EXISTS idx_crawls_parent_competitor;

ALTER TABLE crawls DROP CONSTRAINT IF EXISTS crawls_source_check;

ALTER TABLE crawls
    ADD CONSTRAINT crawls_source_check CHECK (source IN ('manual', 'auto'));

ALTER TABLE crawls
    DROP COLUMN IF EXISTS parent_crawl_id,
    DROP COLUMN IF EXISTS competitor_id;

DROP INDEX IF EXISTS idx_project_competitors_project_id;
DROP TABLE IF EXISTS project_competitors;

ALTER TABLE organization_features DROP CONSTRAINT IF EXISTS chk_organization_features_max_competitors;
ALTER TABLE organization_features DROP COLUMN IF EXISTS max_competitors;

-- +goose StatementEnd
