-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawl_issues
    ADD COLUMN pillar TEXT,
    ADD COLUMN bucket TEXT,
    ADD COLUMN issue_type TEXT;

UPDATE crawl_issues
SET severity = CASE LOWER(TRIM(severity))
    WHEN 'error' THEN 'high'
    WHEN 'warning' THEN 'medium'
    WHEN 'info' THEN 'low'
    ELSE LOWER(TRIM(severity))
END;

UPDATE crawl_issues
SET pillar = CASE
        WHEN code IN ('missing_og_tags', 'missing_structured_data') THEN 'aeo'
        WHEN category = 'pagespeed' THEN 'pagespeed'
        ELSE 'seo'
    END,
    bucket = CASE
        WHEN code IN (
            'missing_title',
            'title_too_long',
            'title_too_short',
            'missing_meta_description',
            'meta_description_too_long',
            'meta_description_too_short'
        ) THEN 'serp_metadata'
        WHEN code IN (
            'missing_h1',
            'multiple_h1',
            'missing_h2_on_long_page'
        ) THEN 'content_structure'
        WHEN code IN ('thin_content') THEN 'content_quality'
        WHEN code IN (
            'missing_canonical',
            'canonical_differs',
            'noindex_page',
            'nofollow_page'
        ) THEN 'indexability'
        WHEN code IN (
            'client_error_status',
            'server_error_status',
            'missing_viewport',
            'missing_lang'
        ) THEN 'technical_seo'
        WHEN code IN (
            'images_missing_alt',
            'images_missing_dimensions'
        ) THEN 'media_optimization'
        WHEN code IN (
            'no_internal_links_out',
            'low_internal_links_out',
            'low_internal_links_in'
        ) THEN 'internal_linking'
        WHEN code IN ('missing_og_tags') THEN 'experience'
        WHEN code IN ('missing_structured_data') THEN 'answerability'
        WHEN code IN ('slow_response_time') THEN 'server_responsiveness'
        WHEN code IN ('moderate_page_size', 'large_page_size') THEN 'page_weight'
        ELSE 'technical_seo'
    END,
    issue_type = code;

ALTER TABLE crawl_issues
    ALTER COLUMN pillar SET NOT NULL,
    ALTER COLUMN bucket SET NOT NULL,
    ALTER COLUMN issue_type SET NOT NULL,
    ADD CONSTRAINT crawl_issues_pillar_check CHECK (pillar IN ('seo', 'aeo', 'pagespeed')),
    ADD CONSTRAINT crawl_issues_severity_check CHECK (severity IN ('low', 'medium', 'high'));

DROP INDEX IF EXISTS idx_crawl_issues_crawl_id_category;
DROP INDEX IF EXISTS idx_crawl_issues_unique_page_code;

CREATE INDEX idx_crawl_issues_crawl_id_pillar ON crawl_issues(crawl_id, pillar);
CREATE INDEX idx_crawl_issues_crawl_id_bucket ON crawl_issues(crawl_id, bucket);
CREATE UNIQUE INDEX idx_crawl_issues_unique_page_issue_type ON crawl_issues(crawl_id, crawl_page_id, issue_type);

ALTER TABLE crawl_issues
    DROP COLUMN category,
    DROP COLUMN code;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_issues
    ADD COLUMN category TEXT,
    ADD COLUMN code TEXT;

UPDATE crawl_issues
SET category = bucket,
    code = issue_type,
    severity = CASE severity
        WHEN 'high' THEN 'error'
        WHEN 'medium' THEN 'warning'
        WHEN 'low' THEN 'info'
        ELSE severity
    END;

DROP INDEX IF EXISTS idx_crawl_issues_crawl_id_pillar;
DROP INDEX IF EXISTS idx_crawl_issues_crawl_id_bucket;
DROP INDEX IF EXISTS idx_crawl_issues_unique_page_issue_type;

ALTER TABLE crawl_issues
    DROP CONSTRAINT IF EXISTS crawl_issues_pillar_check,
    DROP CONSTRAINT IF EXISTS crawl_issues_severity_check;

ALTER TABLE crawl_issues
    ALTER COLUMN category SET NOT NULL,
    ALTER COLUMN code SET NOT NULL;

CREATE INDEX idx_crawl_issues_crawl_id_category ON crawl_issues(crawl_id, category);
CREATE UNIQUE INDEX idx_crawl_issues_unique_page_code ON crawl_issues(crawl_id, crawl_page_id, code);

ALTER TABLE crawl_issues
    DROP COLUMN pillar,
    DROP COLUMN bucket,
    DROP COLUMN issue_type;
-- +goose StatementEnd
