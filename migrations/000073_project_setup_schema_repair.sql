-- +goose Up

-- 000073 is a forward compatibility repair, not a new feature. An early draft
-- of migration 000072 was applied to at least one database before the source
-- migration was finalized, and Goose already records 000072 as applied there,
-- so editing 000072 in place cannot fix that database. This migration converges
-- BOTH possible states onto the final 000072 schema:
--
--   A. old draft 000072 applied: project_setup exists but lacks failed_step,
--      the status check lacks 'ready', requested_by_user_id is
--      NOT NULL ... ON DELETE CASCADE, the trigger function emits the old
--      status event, and ai_worker_jobs_job_type_check lacks
--      'business_profile_bootstrap'.
--   B. final 000072 applied: every object below already has the final shape.
--
-- Every statement is additive or idempotent (IF EXISTS / IF NOT EXISTS /
-- CREATE OR REPLACE), so it is a no-op on a database that is already final.
-- It never deletes rows: existing project_setup rows, crawls, events, and
-- non-bootstrap worker jobs are preserved. Goose runs the whole Up in one
-- transaction, so the repair is atomic.

-- 1. The missing column first, so the failed_step check below can reference it.
ALTER TABLE project_setup ADD COLUMN IF NOT EXISTS failed_step TEXT;

-- 2. Final failed-step allowlist.
ALTER TABLE project_setup DROP CONSTRAINT IF EXISTS project_setup_failed_step_check;
ALTER TABLE project_setup ADD CONSTRAINT project_setup_failed_step_check
    CHECK (failed_step IS NULL OR failed_step IN ('crawling', 'profile_generation', 'prompt_generation', 'visibility'));

-- 3. Final status allowlist, including 'ready'. The old draft was missing it,
--    so any row already in 'ready' stays valid; no row is rewritten.
ALTER TABLE project_setup DROP CONSTRAINT IF EXISTS project_setup_status_check;
ALTER TABLE project_setup ADD CONSTRAINT project_setup_status_check
    CHECK (status IN ('ready', 'crawling', 'profile_generation', 'prompt_generation', 'visibility', 'completed', 'failed'));

-- 4. Deleting the requester must not delete the durable setup row. The old
--    draft column was NOT NULL ... ON DELETE CASCADE; drop the NOT NULL and
--    replace the FK with ON DELETE SET NULL. The constraint name is discovered
--    rather than assumed, so a differently named draft FK is still replaced.
ALTER TABLE project_setup ALTER COLUMN requested_by_user_id DROP NOT NULL;

-- +goose StatementBegin
DO $$
DECLARE
    v_name TEXT;
BEGIN
    SELECT con.conname INTO v_name
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    JOIN pg_attribute att ON att.attrelid = rel.oid AND att.attnum = con.conkey[1]
    WHERE rel.relname = 'project_setup'
      AND con.contype = 'f'
      AND array_length(con.conkey, 1) = 1
      AND att.attname = 'requested_by_user_id'
    LIMIT 1;

    IF v_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE project_setup DROP CONSTRAINT %I', v_name);
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE project_setup
    ADD CONSTRAINT project_setup_requested_by_user_id_fkey
    FOREIGN KEY (requested_by_user_id) REFERENCES users(id) ON DELETE SET NULL;

-- 5. Re-point the trigger at the final event logic (byte-for-byte the final
--    000072 function). CREATE OR REPLACE keeps the existing trigger body
--    valid; only a missing trigger is then created, so no duplicate fires.
-- +goose StatementBegin
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
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_project_setup_organization_event ON project_setup;
CREATE TRIGGER trg_project_setup_organization_event
AFTER INSERT OR UPDATE ON project_setup
FOR EACH ROW EXECUTE FUNCTION organization_event_from_project_setup();

-- 6. One setup row per project must stay unique even if the draft index was
--    missing or named differently.
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_setup_project_id ON project_setup(project_id);

-- 7. The bootstrap flow enqueues a business_profile_bootstrap job; restore the
--    final four-type allowlist. Dropping a CHECK constraint never touches rows.
ALTER TABLE ai_worker_jobs DROP CONSTRAINT IF EXISTS ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs
    ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility', 'business_profile_bootstrap'));

-- +goose Down

-- Intentionally non-destructive. This repair cannot know whether the database
-- it ran against held the old draft 000072 or the final 000072, so there is no
-- single correct inverse: reverting the status/step/job-type allowlists could
-- reject rows that are legal under the other state, and reverting the FK or
-- NOT NULL could resurrect cascade deletion of durable setup rows. Goose still
-- records the version, so rolling all the way past 000072 runs 000072's own
-- Down, which owns the real teardown of this table and constraint.
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
