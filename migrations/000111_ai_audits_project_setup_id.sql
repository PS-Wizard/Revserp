-- +goose Up
-- +goose StatementBegin
ALTER TABLE ai_audits ADD COLUMN project_setup_id UUID REFERENCES project_setup(id) ON DELETE SET NULL;

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
            'crawl_id', NEW.crawl_id,
            'project_setup_id', NEW.project_setup_id
        ));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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

ALTER TABLE ai_audits DROP COLUMN project_setup_id;
-- +goose StatementEnd
