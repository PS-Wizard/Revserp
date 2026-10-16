-- +goose Up
-- +goose StatementBegin
-- Durable once-per-location setup workflow state. One row per location,
-- created by the manager setup POST; a missing row reads as 'ready'.
-- Shared prepare/profile complete first, then the questions->AI audit and
-- map queries->Maps run branches advance independently, so a failure or a
-- quota/credit skip on one branch never blocks or rewrites the other.
-- Completed steps keep their results: a resume only relaunches unfinished
-- work, never a second paid generation, audit, or Maps run.
CREATE TABLE location_setup (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    location_id UUID NOT NULL,
    -- Who requested the setup. Nullable: deleting the requester must not
    -- delete this durable row; a later manager retry re-stamps it.
    requested_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    -- Immutable setup input. Only stored while empty; later POSTs never
    -- overwrite it. The AI profile worker reads it via location_setup.
    differences TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
    -- Last failure reason for the setup as a whole.
    error TEXT,
    prepare_status TEXT NOT NULL DEFAULT 'pending' CHECK (prepare_status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    profile_status TEXT NOT NULL DEFAULT 'pending' CHECK (profile_status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    questions_status TEXT NOT NULL DEFAULT 'pending' CHECK (questions_status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    map_queries_status TEXT NOT NULL DEFAULT 'pending' CHECK (map_queries_status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    ai_visibility_status TEXT NOT NULL DEFAULT 'pending' CHECK (ai_visibility_status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    maps_visibility_status TEXT NOT NULL DEFAULT 'pending' CHECK (maps_visibility_status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    -- Per-step skip/fail reasons keyed by step id, e.g. quota or credit skips.
    step_reasons JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- The location AI audit this setup launched. Null until reserved.
    ai_audit_id UUID REFERENCES ai_audits(id) ON DELETE SET NULL,
    -- The location Maps run this setup launched. Null until reserved.
    maps_run_id UUID REFERENCES local_visibility_runs(id) ON DELETE SET NULL,
    -- Quoted Maps cost recorded when the run is reserved.
    estimated_maps_credits INTEGER CHECK (estimated_maps_credits IS NULL OR estimated_maps_credits > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    FOREIGN KEY (location_id, project_id)
        REFERENCES project_locations(id, project_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_location_setup_location_id ON location_setup(location_id);
CREATE INDEX location_setup_project_idx ON location_setup(project_id);

-- The location setup worker job types. prompt_generation (location AI
-- questions), visibility_run (location AI audit) and local_visibility
-- (location Maps run) already exist and are reused unchanged.
ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility',
        'business_profile_bootstrap', 'local_visibility',
        'location_profile_generation', 'map_query_generation'));

-- At most one ACTIVE (pending or running) setup job per location and type.
-- The running case matters: once a job is claimed, a second POST or tab must
-- not enqueue another paid generation. Completed/failed rows are not active,
-- so a resume after failure may enqueue exactly one fresh job.
CREATE UNIQUE INDEX active_location_setup_job_idx
    ON ai_worker_jobs (project_id, location_id, job_type)
    WHERE job_type IN ('location_profile_generation', 'map_query_generation')
      AND status IN ('pending', 'running')
      AND location_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM location_setup) THEN
        RAISE EXCEPTION 'location setups exist; rollback would destroy history';
    END IF;
END $$;
DROP INDEX IF EXISTS active_location_setup_job_idx;
ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
-- Setup jobs cannot exist under the downgraded constraint; remove any such
-- jobs so the Down never fails on a leftover row.
DELETE FROM ai_worker_jobs
WHERE job_type IN ('location_profile_generation', 'map_query_generation');
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility',
        'business_profile_bootstrap', 'local_visibility'));
DROP INDEX IF EXISTS location_setup_project_idx;
DROP INDEX IF EXISTS idx_location_setup_location_id;
DROP TABLE IF EXISTS location_setup;
-- +goose StatementEnd
