-- +goose Up
-- +goose StatementBegin

-- Durable organization activity log. Producers never call pg_notify directly:
-- a single trigger on this table wakes LISTEN subscribers, which then read
-- rows after their cursor. NOTIFY is a wake hint only and may be coalesced or
-- dropped, so every consumer must fall back to reading durable rows.
CREATE TABLE organization_events (
    id BIGSERIAL PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- Deliberately no project FK: a deleted project must keep its id in the
    -- event row for the full 24h retention window.
    project_id UUID,
    event_type TEXT NOT NULL,
    resource_id UUID,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT organization_events_payload_object CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX idx_organization_events_org_id_id ON organization_events(organization_id, id);
CREATE INDEX idx_organization_events_created_at ON organization_events(created_at);

CREATE OR REPLACE FUNCTION notify_organization_event() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('organization_events', NEW.organization_id::text);
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_organization_events_notify
AFTER INSERT ON organization_events
FOR EACH ROW EXECUTE FUNCTION notify_organization_event();

CREATE OR REPLACE FUNCTION insert_organization_event(
    p_organization_id UUID,
    p_project_id UUID,
    p_event_type TEXT,
    p_resource_id UUID,
    p_payload JSONB
) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_organization_id IS NULL THEN
        RETURN;
    END IF;

    -- BIGSERIAL allocation order is not commit order: concurrent transactions
    -- can commit a higher id before a lower one, stranding the lower id behind
    -- an SSE cursor forever. Serialize per organization BEFORE the INSERT so
    -- ids and commit order agree. Transaction-scoped and reentrant, released on
    -- commit/rollback; different organizations only collide on a rare hash hit.
    PERFORM pg_advisory_xact_lock(hashtextextended(p_organization_id::text, 0x6F72675F65767473::bigint));

    INSERT INTO organization_events (organization_id, project_id, event_type, resource_id, payload)
    VALUES (p_organization_id, p_project_id, p_event_type, p_resource_id, COALESCE(p_payload, '{}'::jsonb));
END;
$$;

-- crawls ---------------------------------------------------------------------
CREATE OR REPLACE FUNCTION organization_event_from_crawl() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
    v_event TEXT;
    v_label TEXT;
    v_payload JSONB;
BEGIN
    SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        v_event := 'crawl.queued';
    ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
        v_event := CASE NEW.status
            WHEN 'running' THEN 'crawl.started'
            WHEN 'completed' THEN 'crawl.completed'
            WHEN 'failed' THEN 'crawl.failed'
            WHEN 'cancelled' THEN 'crawl.cancelled'
            ELSE NULL
        END;
    ELSIF NEW.status = 'running'
        AND (NEW.phase IS DISTINCT FROM OLD.phase
             OR NEW.urls_crawled IS DISTINCT FROM OLD.urls_crawled
             OR NEW.urls_discovered IS DISTINCT FROM OLD.urls_discovered) THEN
        v_event := 'crawl.progress';
    END IF;

    IF v_event IS NULL THEN
        RETURN NULL;
    END IF;

    IF NEW.competitor_id IS NOT NULL THEN
        SELECT COALESCE(NULLIF(pc.name, ''), pc.seed_url)
        INTO v_label
        FROM project_competitors AS pc
        WHERE pc.id = NEW.competitor_id;
    END IF;

    v_payload := jsonb_build_object(
        'status', NEW.status,
        'phase', NEW.phase,
        'urls_discovered', NEW.urls_discovered,
        'urls_crawled', NEW.urls_crawled,
        'source', NEW.source
    );
    IF v_label IS NOT NULL THEN
        v_payload := v_payload || jsonb_build_object('competitor_label', v_label);
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, v_event, NEW.id, v_payload);
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_crawls_organization_event
AFTER INSERT OR UPDATE ON crawls
FOR EACH ROW EXECUTE FUNCTION organization_event_from_crawl();

-- projects -------------------------------------------------------------------
CREATE OR REPLACE FUNCTION organization_event_from_project() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_payload JSONB;
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- A cascading organization delete removes projects too; the org FK would
        -- reject the event and the organization is going away anyway.
        IF NOT EXISTS (SELECT 1 FROM organizations AS o WHERE o.id = OLD.organization_id) THEN
            RETURN NULL;
        END IF;
        v_payload := jsonb_build_object('name', OLD.name, 'base_url', OLD.base_url);
        PERFORM insert_organization_event(OLD.organization_id, OLD.id, 'project.deleted', OLD.id, v_payload);
    ELSIF TG_OP = 'INSERT' THEN
        v_payload := jsonb_build_object('name', NEW.name, 'base_url', NEW.base_url);
        PERFORM insert_organization_event(NEW.organization_id, NEW.id, 'project.created', NEW.id, v_payload);
    ELSIF NEW.name IS DISTINCT FROM OLD.name OR NEW.base_url IS DISTINCT FROM OLD.base_url THEN
        v_payload := jsonb_build_object('name', NEW.name, 'base_url', NEW.base_url);
        PERFORM insert_organization_event(NEW.organization_id, NEW.id, 'project.updated', NEW.id, v_payload);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_projects_organization_event
AFTER INSERT OR UPDATE OR DELETE ON projects
FOR EACH ROW EXECUTE FUNCTION organization_event_from_project();

-- ai_audits ------------------------------------------------------------------
CREATE OR REPLACE FUNCTION organization_event_from_ai_audit() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
    v_event TEXT;
BEGIN
    SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
        v_event := CASE NEW.status
            WHEN 'queued' THEN 'ai_audit.queued'
            WHEN 'running' THEN 'ai_audit.started'
            WHEN 'completed' THEN 'ai_audit.completed'
            WHEN 'completed_with_failures' THEN 'ai_audit.completed_with_failures'
            WHEN 'failed' THEN 'ai_audit.failed'
            ELSE NULL
        END;
    END IF;

    IF v_event IS NULL THEN
        RETURN NULL;
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, v_event, NEW.id,
        jsonb_build_object(
            'status', NEW.status,
            'score', NEW.score,
            'error', NEW.error_message,
            'crawl_id', NEW.crawl_id
        ));
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_ai_audits_organization_event
AFTER INSERT OR UPDATE ON ai_audits
FOR EACH ROW EXECUTE FUNCTION organization_event_from_ai_audit();

-- ai_audit_runs: progress only. Lifecycle stays on ai_audits (authoritative).
CREATE OR REPLACE FUNCTION organization_event_from_ai_audit_run() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
    v_project UUID;
    v_completed BIGINT;
    v_total BIGINT;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NULL;
    END IF;

    SELECT aa.project_id, p.organization_id INTO v_project, v_org
    FROM ai_audits AS aa
    INNER JOIN projects AS p ON p.id = aa.project_id
    WHERE aa.id = NEW.audit_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT count(*), count(*) FILTER (WHERE status IN ('success', 'failed'))
    INTO v_total, v_completed
    FROM ai_audit_runs
    WHERE audit_id = NEW.audit_id;

    PERFORM insert_organization_event(v_org, v_project, 'ai_audit.progress', NEW.audit_id,
        jsonb_build_object('completed', v_completed, 'total', v_total));
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_ai_audit_runs_organization_event
AFTER INSERT OR UPDATE ON ai_audit_runs
FOR EACH ROW EXECUTE FUNCTION organization_event_from_ai_audit_run();

-- ai_worker_jobs: prompt_generation only. visibility_run/maps_visibility are
-- covered authoritatively by ai_audits / maps_visibility_checks, so emitting
-- them here would duplicate lifecycle events.
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

CREATE TRIGGER trg_ai_worker_jobs_organization_event
AFTER INSERT OR UPDATE ON ai_worker_jobs
FOR EACH ROW EXECUTE FUNCTION organization_event_from_ai_worker_job();

-- maps_visibility_checks -----------------------------------------------------
CREATE OR REPLACE FUNCTION organization_event_from_maps_visibility() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
    v_event TEXT;
BEGIN
    SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
        v_event := CASE NEW.status
            WHEN 'queued' THEN 'maps_visibility.queued'
            WHEN 'running' THEN 'maps_visibility.started'
            WHEN 'completed' THEN 'maps_visibility.completed'
            WHEN 'failed' THEN 'maps_visibility.failed'
            ELSE NULL
        END;
    END IF;

    IF v_event IS NULL THEN
        RETURN NULL;
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, v_event, NEW.id,
        jsonb_build_object('status', NEW.status, 'error', NEW.error));
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_maps_visibility_checks_organization_event
AFTER INSERT OR UPDATE ON maps_visibility_checks
FOR EACH ROW EXECUTE FUNCTION organization_event_from_maps_visibility();

-- project_business_profile ---------------------------------------------------
CREATE OR REPLACE FUNCTION organization_event_from_business_profile() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
BEGIN
    SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
    IF v_org IS NULL THEN
        RETURN NULL;
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, 'business_profile.updated', NEW.id,
        jsonb_build_object('brand_name', NEW.brand_name));
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_project_business_profile_organization_event
AFTER INSERT OR UPDATE ON project_business_profile
FOR EACH ROW EXECUTE FUNCTION organization_event_from_business_profile();

-- project_competitors --------------------------------------------------------
CREATE OR REPLACE FUNCTION organization_event_from_project_competitor() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_org UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- A project delete cascades to its competitors; project.deleted already
        -- reports that, so skip the cascaded rows.
        IF NOT EXISTS (SELECT 1 FROM projects AS p WHERE p.id = OLD.project_id) THEN
            RETURN NULL;
        END IF;
        SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = OLD.project_id;
        IF v_org IS NULL THEN
            RETURN NULL;
        END IF;
        PERFORM insert_organization_event(v_org, OLD.project_id, 'project_competitor.deleted', OLD.id,
            jsonb_build_object('seed_url', OLD.seed_url, 'name', OLD.name));
    ELSE
        SELECT p.organization_id INTO v_org FROM projects AS p WHERE p.id = NEW.project_id;
        IF v_org IS NULL THEN
            RETURN NULL;
        END IF;
        PERFORM insert_organization_event(v_org, NEW.project_id, 'project_competitor.created', NEW.id,
            jsonb_build_object('seed_url', NEW.seed_url, 'name', NEW.name));
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_project_competitors_organization_event
AFTER INSERT OR DELETE ON project_competitors
FOR EACH ROW EXECUTE FUNCTION organization_event_from_project_competitor();

-- MCP crawl origin -----------------------------------------------------------
ALTER TABLE crawls DROP CONSTRAINT IF EXISTS crawls_source_check;
ALTER TABLE crawls
    ADD CONSTRAINT crawls_source_check CHECK (source IN ('manual', 'auto', 'competitor', 'mcp'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE crawls DROP CONSTRAINT IF EXISTS crawls_source_check;
ALTER TABLE crawls
    ADD CONSTRAINT crawls_source_check CHECK (source IN ('manual', 'auto', 'competitor'));

DROP TRIGGER IF EXISTS trg_project_competitors_organization_event ON project_competitors;
DROP FUNCTION IF EXISTS organization_event_from_project_competitor();

DROP TRIGGER IF EXISTS trg_project_business_profile_organization_event ON project_business_profile;
DROP FUNCTION IF EXISTS organization_event_from_business_profile();

DROP TRIGGER IF EXISTS trg_maps_visibility_checks_organization_event ON maps_visibility_checks;
DROP FUNCTION IF EXISTS organization_event_from_maps_visibility();

DROP TRIGGER IF EXISTS trg_ai_worker_jobs_organization_event ON ai_worker_jobs;
DROP FUNCTION IF EXISTS organization_event_from_ai_worker_job();

DROP TRIGGER IF EXISTS trg_ai_audit_runs_organization_event ON ai_audit_runs;
DROP FUNCTION IF EXISTS organization_event_from_ai_audit_run();

DROP TRIGGER IF EXISTS trg_ai_audits_organization_event ON ai_audits;
DROP FUNCTION IF EXISTS organization_event_from_ai_audit();

DROP TRIGGER IF EXISTS trg_projects_organization_event ON projects;
DROP FUNCTION IF EXISTS organization_event_from_project();

DROP TRIGGER IF EXISTS trg_crawls_organization_event ON crawls;
DROP FUNCTION IF EXISTS organization_event_from_crawl();

DROP FUNCTION IF EXISTS insert_organization_event(UUID, UUID, TEXT, UUID, JSONB);

DROP TRIGGER IF EXISTS trg_organization_events_notify ON organization_events;
DROP FUNCTION IF EXISTS notify_organization_event();

DROP INDEX IF EXISTS idx_organization_events_created_at;
DROP INDEX IF EXISTS idx_organization_events_org_id_id;
DROP TABLE IF EXISTS organization_events;

-- +goose StatementEnd