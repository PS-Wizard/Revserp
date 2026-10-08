-- +goose Up
-- +goose StatementBegin
-- Location AI questions are independent per location. The parent
-- project_ai_questions row is never read or written by the location path.
CREATE TABLE location_ai_questions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    location_id UUID NOT NULL UNIQUE,
    questions JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(questions) = 'array'),
    generation_model TEXT NOT NULL DEFAULT '',
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, project_id)
        REFERENCES project_locations(id, project_id) ON DELETE CASCADE
);
CREATE INDEX location_ai_questions_project_idx ON location_ai_questions(project_id);

-- A prompt_generation job may carry a location scope. The composite FK keeps the
-- job bound to the same project's location; parent jobs keep location_id NULL
-- (MATCH SIMPLE skips the check when location_id is NULL).
ALTER TABLE ai_worker_jobs ADD COLUMN location_id UUID;
ALTER TABLE ai_worker_jobs ADD CONSTRAINT ai_worker_jobs_location_project_fkey
    FOREIGN KEY (location_id, project_id)
    REFERENCES project_locations(id, project_id) ON DELETE CASCADE;

-- At most one ACTIVE (pending or running) location question job per location.
-- The running case matters: once a job is claimed, a second click or tab must
-- not enqueue another paid local generation. Completed/failed rows are not
-- active, so a fresh regeneration is allowed. Parent jobs (location_id NULL)
-- and other locations are excluded, so local A/B and parent never coalesce.
CREATE UNIQUE INDEX active_location_prompt_idx
    ON ai_worker_jobs (project_id, location_id)
    WHERE job_type = 'prompt_generation' AND status IN ('pending', 'running') AND location_id IS NOT NULL;

-- Keep the same prompt_generation.* lifecycle for parent and location jobs and
-- carry the location scope in the payload so the client can match it exactly
-- (NULL for parent jobs).
CREATE OR REPLACE FUNCTION organization_event_from_ai_worker_job() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
    v_event TEXT;
BEGIN
    IF NEW.job_type <> 'prompt_generation' THEN
        RETURN NULL;
    END IF;

    SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
        v_event := CASE NEW.status
            WHEN 'pending' THEN 'prompt_generation.queued'
            WHEN 'running' THEN 'prompt_generation.started'
            WHEN 'completed' THEN 'prompt_generation.completed'
            WHEN 'failed' THEN 'prompt_generation.failed'
            ELSE NULL
        END;
    END IF;

    IF v_event IS NULL THEN
        RETURN NULL;
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, v_event, NEW.id,
        jsonb_build_object('status', NEW.status, 'error', NEW.error_message,
            'location_id', NEW.location_id));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION organization_event_from_ai_worker_job() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
    v_event TEXT;
BEGIN
    IF NEW.job_type <> 'prompt_generation' THEN
        RETURN NULL;
    END IF;

    SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
        v_event := CASE NEW.status
            WHEN 'pending' THEN 'prompt_generation.queued'
            WHEN 'running' THEN 'prompt_generation.started'
            WHEN 'completed' THEN 'prompt_generation.completed'
            WHEN 'failed' THEN 'prompt_generation.failed'
            ELSE NULL
        END;
    END IF;

    IF v_event IS NULL THEN
        RETURN NULL;
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, v_event, NEW.id,
        jsonb_build_object('status', NEW.status, 'error', NEW.error_message));
    RETURN NULL;
END;
$$;

DROP INDEX IF EXISTS active_location_prompt_idx;
ALTER TABLE ai_worker_jobs DROP CONSTRAINT IF EXISTS ai_worker_jobs_location_project_fkey;
ALTER TABLE ai_worker_jobs DROP COLUMN IF EXISTS location_id;
DROP TABLE IF EXISTS location_ai_questions;
-- +goose StatementEnd
