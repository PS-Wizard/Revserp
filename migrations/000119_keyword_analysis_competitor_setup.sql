-- +goose Up
-- +goose StatementBegin

-- Project setup gains the keyword_analysis step between profile_generation and
-- prompt_generation. Existing rows keep their status; only the allowlist
-- widens.
ALTER TABLE project_setup DROP CONSTRAINT IF EXISTS project_setup_status_check;
ALTER TABLE project_setup ADD CONSTRAINT project_setup_status_check
    CHECK (status IN ('ready', 'crawling', 'profile_generation', 'keyword_analysis', 'prompt_generation', 'visibility', 'completed', 'failed'));
ALTER TABLE project_setup DROP CONSTRAINT IF EXISTS project_setup_failed_step_check;
ALTER TABLE project_setup ADD CONSTRAINT project_setup_failed_step_check
    CHECK (failed_step IS NULL OR failed_step IN ('crawling', 'profile_generation', 'keyword_analysis', 'prompt_generation', 'visibility'));

-- AI worker jobs gain the keyword analysis family. keyword_analysis serves the
-- project setup; the competitor_* pair serves one competitor initialization.
-- competitor_id scopes a job to a roster row; NULL keeps project semantics.
ALTER TABLE ai_worker_jobs DROP CONSTRAINT IF EXISTS ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility',
        'business_profile_bootstrap', 'local_visibility',
        'location_profile_generation', 'map_query_generation',
        'keyword_analysis', 'competitor_keyword_generation', 'competitor_keyword_analysis'));
ALTER TABLE ai_worker_jobs
    ADD COLUMN competitor_id UUID REFERENCES project_competitors(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_ai_worker_jobs_competitor_id ON ai_worker_jobs(competitor_id);

-- AI-estimated metrics per project keyword phrase. Estimates are model guesses
-- grounded in crawled content, never measured search data; every metric stays
-- nullable so an unknown estimate is NULL rather than a fabricated zero.
CREATE TABLE project_keyword_analysis (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    keyword TEXT NOT NULL CHECK (length(btrim(keyword)) > 0),
    normalized_keyword TEXT NOT NULL CHECK (length(btrim(normalized_keyword)) > 0),
    kind TEXT NOT NULL CHECK (kind IN ('brand', 'non_brand')),
    rank INTEGER CHECK (rank IS NULL OR (rank >= 1 AND rank <= 100)),
    traffic BIGINT CHECK (traffic IS NULL OR traffic >= 0),
    volume BIGINT CHECK (volume IS NULL OR volume >= 0),
    kd DOUBLE PRECISION CHECK (kd IS NULL OR (kd >= 0 AND kd <= 100)),
    cpc DOUBLE PRECISION CHECK (cpc IS NULL OR cpc >= 0),
    results_count BIGINT CHECK (results_count IS NULL OR results_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, normalized_keyword)
);
CREATE INDEX project_keyword_analysis_project_idx ON project_keyword_analysis(project_id);

-- Competitor keywords plus their AI estimates in one row: keyword_generation
-- writes the phrases with NULL metrics, keyword_analysis fills the metrics.
CREATE TABLE competitor_keyword_analysis (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    competitor_id UUID NOT NULL REFERENCES project_competitors(id) ON DELETE CASCADE,
    keyword TEXT NOT NULL CHECK (length(btrim(keyword)) > 0),
    normalized_keyword TEXT NOT NULL CHECK (length(btrim(normalized_keyword)) > 0),
    kind TEXT NOT NULL CHECK (kind IN ('brand', 'non_brand')),
    rank INTEGER CHECK (rank IS NULL OR (rank >= 1 AND rank <= 100)),
    traffic BIGINT CHECK (traffic IS NULL OR traffic >= 0),
    volume BIGINT CHECK (volume IS NULL OR volume >= 0),
    kd DOUBLE PRECISION CHECK (kd IS NULL OR (kd >= 0 AND kd <= 100)),
    cpc DOUBLE PRECISION CHECK (cpc IS NULL OR cpc >= 0),
    results_count BIGINT CHECK (results_count IS NULL OR results_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (competitor_id, normalized_keyword)
);
CREATE INDEX competitor_keyword_analysis_competitor_idx ON competitor_keyword_analysis(competitor_id);

-- Durable once-per-competitor initialization. A missing row reads as 'ready';
-- the POST creates it in 'running'. Steps advance crawl -> keyword_generation
-- -> keyword_analysis; the crawl step reconciles lazily from the child crawl
-- row so the crawler itself never learns about this workflow.
CREATE TABLE competitor_setup (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    competitor_id UUID NOT NULL REFERENCES project_competitors(id) ON DELETE CASCADE,
    requested_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    parent_crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
    crawl_status TEXT NOT NULL DEFAULT 'pending' CHECK (crawl_status IN ('pending', 'running', 'completed', 'failed')),
    keyword_generation_status TEXT NOT NULL DEFAULT 'pending' CHECK (keyword_generation_status IN ('pending', 'running', 'completed', 'failed')),
    keyword_analysis_status TEXT NOT NULL DEFAULT 'pending' CHECK (keyword_analysis_status IN ('pending', 'running', 'completed', 'failed')),
    error TEXT,
    failed_step TEXT CHECK (failed_step IS NULL OR failed_step IN ('crawl', 'keyword_generation', 'keyword_analysis')),
    step_reasons JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (competitor_id)
);
CREATE INDEX competitor_setup_project_idx ON competitor_setup(project_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM competitor_setup) THEN
        RAISE EXCEPTION 'competitor setups exist; rollback would destroy history';
    END IF;
    IF EXISTS (SELECT 1 FROM project_keyword_analysis) THEN
        RAISE EXCEPTION 'project keyword analyses exist; rollback would destroy history';
    END IF;
    IF EXISTS (SELECT 1 FROM competitor_keyword_analysis) THEN
        RAISE EXCEPTION 'competitor keyword analyses exist; rollback would destroy history';
    END IF;
END $$;
DROP INDEX IF EXISTS competitor_setup_project_idx;
DROP TABLE IF EXISTS competitor_setup;
DROP INDEX IF EXISTS competitor_keyword_analysis_competitor_idx;
DROP TABLE IF EXISTS competitor_keyword_analysis;
DROP INDEX IF EXISTS project_keyword_analysis_project_idx;
DROP TABLE IF EXISTS project_keyword_analysis;
DROP INDEX IF EXISTS idx_ai_worker_jobs_competitor_id;
ALTER TABLE ai_worker_jobs DROP COLUMN IF EXISTS competitor_id;
DELETE FROM ai_worker_jobs
WHERE job_type IN ('keyword_analysis', 'competitor_keyword_generation', 'competitor_keyword_analysis');
ALTER TABLE ai_worker_jobs DROP CONSTRAINT IF EXISTS ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility',
        'business_profile_bootstrap', 'local_visibility',
        'location_profile_generation', 'map_query_generation'));
ALTER TABLE project_setup DROP CONSTRAINT IF EXISTS project_setup_failed_step_check;
ALTER TABLE project_setup ADD CONSTRAINT project_setup_failed_step_check
    CHECK (failed_step IS NULL OR failed_step IN ('crawling', 'profile_generation', 'prompt_generation', 'visibility'));
ALTER TABLE project_setup DROP CONSTRAINT IF EXISTS project_setup_status_check;
ALTER TABLE project_setup ADD CONSTRAINT project_setup_status_check
    CHECK (status IN ('ready', 'crawling', 'profile_generation', 'prompt_generation', 'visibility', 'completed', 'failed'));
-- +goose StatementEnd
