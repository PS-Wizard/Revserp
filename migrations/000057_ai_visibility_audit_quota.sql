-- +goose Up
-- +goose StatementBegin
ALTER TABLE organization_features ADD COLUMN ai_visibility_audit_monthly_limit INTEGER NOT NULL DEFAULT 10;
ALTER TABLE organization_features ADD CONSTRAINT chk_organization_features_ai_visibility_audit_monthly_limit CHECK (ai_visibility_audit_monthly_limit >= 0);
ALTER TABLE ai_workspace_monthly_usage ADD COLUMN used_audits INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ai_workspace_monthly_usage ADD CONSTRAINT chk_ai_workspace_monthly_usage_used_audits CHECK (used_audits >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ai_workspace_monthly_usage DROP CONSTRAINT IF EXISTS chk_ai_workspace_monthly_usage_used_audits;
ALTER TABLE ai_workspace_monthly_usage DROP COLUMN IF EXISTS used_audits;
ALTER TABLE organization_features DROP CONSTRAINT IF EXISTS chk_organization_features_ai_visibility_audit_monthly_limit;
ALTER TABLE organization_features DROP COLUMN IF EXISTS ai_visibility_audit_monthly_limit;
-- +goose StatementEnd
