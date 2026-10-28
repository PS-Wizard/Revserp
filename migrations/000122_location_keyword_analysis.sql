-- +goose Up
-- +goose StatementBegin

-- Location keyword estimates mirror project_keyword_analysis per location:
-- AI-generated rank/traffic/volume/KD/CPC for the location's brand and
-- non-brand keywords, filled by the location setup keyword_analysis step.
CREATE TABLE location_keyword_analysis (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL REFERENCES project_locations(id) ON DELETE CASCADE,
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
    UNIQUE (location_id, normalized_keyword)
);
CREATE INDEX location_keyword_analysis_location_idx ON location_keyword_analysis(location_id);

-- The location setup keyword_analysis step runs between profile and the
-- questions/map-queries branches, mirroring project setup ordering.
ALTER TABLE location_setup
    ADD COLUMN keyword_analysis_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (keyword_analysis_status IN ('pending', 'running', 'completed', 'failed', 'skipped'));

ALTER TABLE ai_worker_jobs DROP CONSTRAINT IF EXISTS ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility',
        'business_profile_bootstrap', 'local_visibility',
        'location_profile_generation', 'map_query_generation',
        'keyword_analysis', 'competitor_keyword_generation', 'competitor_keyword_analysis',
        'location_keyword_analysis'));

DROP INDEX IF EXISTS active_location_setup_job_idx;
CREATE UNIQUE INDEX active_location_setup_job_idx
    ON ai_worker_jobs (project_id, location_id, job_type)
    WHERE job_type IN ('location_profile_generation', 'map_query_generation', 'location_keyword_analysis')
      AND status IN ('pending', 'running')
      AND location_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM location_keyword_analysis) THEN
        RAISE EXCEPTION 'location keyword analyses exist; rollback would destroy history';
    END IF;
END $$;
DROP INDEX IF EXISTS active_location_setup_job_idx;
CREATE UNIQUE INDEX active_location_setup_job_idx
    ON ai_worker_jobs (project_id, location_id, job_type)
    WHERE job_type IN ('location_profile_generation', 'map_query_generation')
      AND status IN ('pending', 'running')
      AND location_id IS NOT NULL;
ALTER TABLE ai_worker_jobs DROP CONSTRAINT IF EXISTS ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility',
        'business_profile_bootstrap', 'local_visibility',
        'location_profile_generation', 'map_query_generation',
        'keyword_analysis', 'competitor_keyword_generation', 'competitor_keyword_analysis'));
DELETE FROM ai_worker_jobs WHERE job_type = 'location_keyword_analysis';
ALTER TABLE location_setup DROP COLUMN IF EXISTS keyword_analysis_status;
DROP INDEX IF EXISTS location_keyword_analysis_location_idx;
DROP TABLE IF EXISTS location_keyword_analysis;
-- +goose StatementEnd
