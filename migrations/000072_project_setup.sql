-- +goose Up
-- +goose StatementBegin

-- Durable once-per-project initial setup workflow state. One row per project;
-- project creation inserts it in 'ready' and the owner's POST starts the
-- initial crawl, moving it to 'crawling'. Workers advance it through
-- profile_generation, prompt_generation and visibility.
CREATE TABLE project_setup (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Who requested the setup. Nullable: deleting the requester account must
    -- not delete this durable row; a new owner retry re-stamps it.
    requested_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    -- The initial crawl this setup started. Null until the crawl row exists.
    crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    status TEXT NOT NULL,
    -- Last failure reason for the setup as a whole.
    error TEXT,
    -- Step that failed, so a later retry orchestration knows where to resume.
    failed_step TEXT,
    -- Set when visibility has nothing to run (e.g. profile has no location).
    visibility_skip_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT project_setup_status_check CHECK (
        status IN ('ready', 'crawling', 'profile_generation', 'prompt_generation', 'visibility', 'completed', 'failed')
    ),
    CONSTRAINT project_setup_failed_step_check CHECK (
        failed_step IS NULL OR failed_step IN ('crawling', 'profile_generation', 'prompt_generation', 'visibility')
    )
);

CREATE UNIQUE INDEX idx_project_setup_project_id ON project_setup(project_id);

-- project_setup --------------------------------------------------------------
-- SSE is state-only: rows are the durable record, this trigger is the single
-- writer of setup events. Transitions happen wherever the setup row changes.
CREATE OR REPLACE FUNCTION organization_event_from_project_setup() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_event TEXT;
    v_payload JSONB;
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- The row is created in 'ready' at project creation; it is not a
        -- started setup yet, so no event is emitted for this insert.
        IF NEW.status = 'ready' THEN
            RETURN NULL;
        END IF;
        v_event := 'project_setup.started';
    ELSIF OLD.status = 'ready' AND NEW.status = 'crawling' THEN
        v_event := 'project_setup.started';
    ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
        v_event := CASE NEW.status
            WHEN 'completed' THEN 'project_setup.completed'
            WHEN 'failed' THEN 'project_setup.failed'
            ELSE 'project_setup.status_changed'
        END;
    ELSE
        RETURN NULL;
    END IF;

    v_payload := jsonb_build_object(
        'status', NEW.status,
        'error', NEW.error,
        'failed_step', NEW.failed_step,
        'crawl_id', NEW.crawl_id,
        'visibility_skip_reason', NEW.visibility_skip_reason
    );

    PERFORM insert_organization_event(NEW.organization_id, NEW.project_id, v_event, NEW.id, v_payload);
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_project_setup_organization_event
AFTER INSERT OR UPDATE ON project_setup
FOR EACH ROW EXECUTE FUNCTION organization_event_from_project_setup();

-- project_setup enqueues a business_profile_bootstrap job when the initial
-- crawl completes, so the worker job type must be permitted.
ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs
    ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility', 'business_profile_bootstrap'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_project_setup_organization_event ON project_setup;
DROP FUNCTION IF EXISTS organization_event_from_project_setup();

DROP INDEX IF EXISTS idx_project_setup_project_id;
DROP TABLE IF EXISTS project_setup;

ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
-- The bootstrap job type cannot exist under the downgraded constraint; remove
-- any such jobs so the Down never fails on a leftover row.
DELETE FROM ai_worker_jobs WHERE job_type = 'business_profile_bootstrap';
ALTER TABLE ai_worker_jobs
    ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility'));

-- +goose StatementEnd
