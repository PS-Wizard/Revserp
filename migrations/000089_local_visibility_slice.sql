-- +goose Up
-- +goose StatementBegin
CREATE TABLE project_locations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(btrim(name)) > 0),
    place_id TEXT NOT NULL CHECK (length(btrim(place_id)) > 0),
    latitude DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    queries JSONB NOT NULL CHECK (jsonb_typeof(queries) = 'array' AND jsonb_array_length(queries) = 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX project_locations_project_idx ON project_locations(project_id);

CREATE TABLE organization_maps_credit_budgets (
    organization_id UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    remaining_credits BIGINT NOT NULL DEFAULT 0,
    reserved_credits BIGINT NOT NULL DEFAULT 0 CHECK (reserved_credits >= 0),
    spent_credits BIGINT NOT NULL DEFAULT 0 CHECK (spent_credits >= 0)
);
CREATE TABLE platform_maps_credit_budget (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    remaining_credits BIGINT NOT NULL DEFAULT 0,
    reserved_credits BIGINT NOT NULL DEFAULT 0 CHECK (reserved_credits >= 0),
    spent_credits BIGINT NOT NULL DEFAULT 0 CHECK (spent_credits >= 0)
);
INSERT INTO platform_maps_credit_budget(id) VALUES(TRUE);

CREATE TABLE local_visibility_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id UUID NOT NULL REFERENCES project_locations(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','completed','partial','failed')),
    radius_m INTEGER NOT NULL CHECK (radius_m BETWEEN 1000 AND 25000),
    snapshot JSONB NOT NULL,
    expected_credits INTEGER NOT NULL CHECK (expected_credits > 0),
    reserved_credits INTEGER NOT NULL CHECK (reserved_credits >= 0),
    credits_used INTEGER NOT NULL DEFAULT 0 CHECK (credits_used >= 0),
    retry_credits INTEGER NOT NULL DEFAULT 0 CHECK (retry_credits >= 0),
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX local_visibility_runs_inflight_idx ON local_visibility_runs(location_id)
    WHERE status IN ('queued','running');
CREATE INDEX local_visibility_runs_latest_idx ON local_visibility_runs(location_id,created_at DESC);

CREATE TABLE local_run_cells (
    run_id UUID NOT NULL REFERENCES local_visibility_runs(id) ON DELETE CASCADE,
    query_index SMALLINT NOT NULL CHECK (query_index BETWEEN 0 AND 4),
    point_index SMALLINT NOT NULL CHECK (point_index BETWEEN 0 AND 8),
    started_at TIMESTAMPTZ,
    PRIMARY KEY(run_id,query_index,point_index)
);
CREATE TABLE local_visibility_results (
    run_id UUID NOT NULL,
    query_index SMALLINT NOT NULL,
    point_index SMALLINT NOT NULL,
    call_status TEXT NOT NULL CHECK (call_status IN ('request_failed','success_empty','success_nonempty')),
    match_status TEXT NOT NULL CHECK (match_status IN ('found','absent','unknown')),
    rank INTEGER,
    credits INTEGER NOT NULL CHECK (credits >= 0),
    credit_known BOOLEAN NOT NULL,
    raw_response JSONB,
    error TEXT,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(run_id,query_index,point_index),
    FOREIGN KEY(run_id,query_index,point_index) REFERENCES local_run_cells(run_id,query_index,point_index) ON DELETE CASCADE,
    CHECK ((match_status = 'found' AND rank IS NOT NULL AND rank > 0) OR (match_status <> 'found' AND rank IS NULL)),
    CHECK (call_status <> 'request_failed' OR match_status = 'unknown')
);

CREATE FUNCTION protect_local_visibility_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.location_id IS DISTINCT FROM OLD.location_id
       OR NEW.radius_m IS DISTINCT FROM OLD.radius_m
       OR NEW.snapshot IS DISTINCT FROM OLD.snapshot
       OR NEW.expected_credits IS DISTINCT FROM OLD.expected_credits THEN
        RAISE EXCEPTION 'local visibility measurement snapshot is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER local_visibility_snapshot_immutable BEFORE UPDATE ON local_visibility_runs
    FOR EACH ROW EXECUTE FUNCTION protect_local_visibility_snapshot();
CREATE FUNCTION protect_local_visibility_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'local visibility outcomes are immutable';
END;
$$;
CREATE TRIGGER local_visibility_result_immutable BEFORE UPDATE ON local_visibility_results
    FOR EACH ROW EXECUTE FUNCTION protect_local_visibility_result();

ALTER TABLE ai_worker_jobs ADD COLUMN local_run_id UUID REFERENCES local_visibility_runs(id) ON DELETE CASCADE;
ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK(job_type IN ('prompt_generation','visibility_run','maps_visibility','business_profile_bootstrap','local_visibility'));
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_local_run_check
    CHECK ((job_type = 'local_visibility') = (local_run_id IS NOT NULL));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ai_worker_jobs WHERE job_type = 'local_visibility';
ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_local_run_check;
ALTER TABLE ai_worker_jobs DROP COLUMN local_run_id;
ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK(job_type IN ('prompt_generation','visibility_run','maps_visibility','business_profile_bootstrap'));
DROP TABLE local_visibility_results;
DROP TABLE local_run_cells;
DROP TABLE local_visibility_runs;
DROP FUNCTION protect_local_visibility_result();
DROP FUNCTION protect_local_visibility_snapshot();
DROP TABLE platform_maps_credit_budget;
DROP TABLE organization_maps_credit_budgets;
DROP TABLE project_locations;
-- +goose StatementEnd
