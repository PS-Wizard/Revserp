-- +goose Up
-- +goose StatementBegin

-- Checked competitor keywords persist per project. One row is one checked
-- keyword: checking inserts, unchecking deletes, so no checked flag column
-- is needed. Only non-brand competitor keywords are selectable; the kind
-- check rejects brand rows at the database level.
CREATE TABLE project_competitor_keyword_selections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    competitor_id UUID NOT NULL REFERENCES project_competitors(id) ON DELETE CASCADE,
    keyword TEXT NOT NULL CHECK (length(btrim(keyword)) > 0),
    normalized_keyword TEXT NOT NULL CHECK (length(btrim(normalized_keyword)) > 0),
    kind TEXT NOT NULL DEFAULT 'non_brand' CHECK (kind IN ('non_brand')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (competitor_id, normalized_keyword)
);
CREATE INDEX project_competitor_keyword_selections_project_idx ON project_competitor_keyword_selections(project_id);
CREATE INDEX project_competitor_keyword_selections_competitor_idx ON project_competitor_keyword_selections(competitor_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS project_competitor_keyword_selections_competitor_idx;
DROP INDEX IF EXISTS project_competitor_keyword_selections_project_idx;
DROP TABLE IF EXISTS project_competitor_keyword_selections;
-- +goose StatementEnd
