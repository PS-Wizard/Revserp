-- +goose Up
-- +goose StatementBegin
ALTER TABLE crawls ADD COLUMN analysis_progress JSONB;

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
             OR NEW.urls_discovered IS DISTINCT FROM OLD.urls_discovered
             OR NEW.analysis_progress IS DISTINCT FROM OLD.analysis_progress) THEN
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
        'source', NEW.source,
        'analysis_progress', NEW.analysis_progress
    );
    IF v_label IS NOT NULL THEN
        v_payload := v_payload || jsonb_build_object('competitor_label', v_label);
    END IF;

    PERFORM insert_organization_event(v_org, NEW.project_id, v_event, NEW.id, v_payload);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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

ALTER TABLE crawls DROP COLUMN IF EXISTS analysis_progress;
-- +goose StatementEnd
