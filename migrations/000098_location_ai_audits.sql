-- +goose Up
-- +goose StatementBegin
-- Additive only: existing project audits keep location_id NULL.
ALTER TABLE ai_audits ADD COLUMN location_id UUID;
ALTER TABLE ai_audits ADD CONSTRAINT ai_audits_location_id_project_id_fkey
    FOREIGN KEY (location_id, project_id)
    REFERENCES project_locations(id, project_id);

-- NULL means "not decided": old, failed and project-audit runs. Only a completed
-- location run writes TRUE/FALSE.
ALTER TABLE ai_audit_runs ADD COLUMN mentioned_branch BOOLEAN;

DROP INDEX IF EXISTS idx_ai_audits_one_active_per_crawl;
CREATE UNIQUE INDEX idx_ai_audits_one_active_per_crawl
ON ai_audits (project_id, crawl_id)
WHERE status IN ('queued', 'running')
  AND crawl_id IS NOT NULL
  AND location_id IS NULL;

CREATE UNIQUE INDEX idx_ai_audits_one_active_per_location
ON ai_audits (project_id, location_id)
WHERE status IN ('queued', 'running')
  AND location_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM ai_audits WHERE location_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM ai_audit_runs WHERE mentioned_branch IS NOT NULL) THEN
        RAISE EXCEPTION 'location audits or branch evidence exist; rollback would destroy history';
    END IF;
END $$;
DROP INDEX IF EXISTS idx_ai_audits_one_active_per_location;
DROP INDEX IF EXISTS idx_ai_audits_one_active_per_crawl;
CREATE UNIQUE INDEX idx_ai_audits_one_active_per_crawl
ON ai_audits (project_id, crawl_id)
WHERE status IN ('queued', 'running')
  AND crawl_id IS NOT NULL;
ALTER TABLE ai_audit_runs DROP COLUMN IF EXISTS mentioned_branch;
ALTER TABLE ai_audits DROP CONSTRAINT IF EXISTS ai_audits_location_id_project_id_fkey;
ALTER TABLE ai_audits DROP COLUMN IF EXISTS location_id;
-- +goose StatementEnd
