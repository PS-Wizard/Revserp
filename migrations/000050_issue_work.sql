-- +goose Up
-- +goose StatementBegin
CREATE TABLE issue_work_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('page', 'group', 'site')),
    subject_key TEXT NOT NULL,
    pillar TEXT NOT NULL,
    bucket TEXT NOT NULL,
    issue_type TEXT NOT NULL,
    source_crawl_issue_id UUID REFERENCES crawl_issues(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'open',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One active work item per project, subject, and issue identity.
CREATE UNIQUE INDEX idx_issue_work_items_identity
    ON issue_work_items(project_id, subject_kind, subject_key, pillar, bucket, issue_type);

CREATE INDEX idx_issue_work_items_project_id ON issue_work_items(project_id);

CREATE TABLE issue_work_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    work_item_id UUID NOT NULL REFERENCES issue_work_items(id) ON DELETE CASCADE,
    source_crawl_id UUID NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'awaiting_verification'
        CHECK (status IN ('awaiting_verification', 'fixed', 'still_open', 'not_verified')),
    verification_crawl_id UUID REFERENCES crawls(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at TIMESTAMPTZ,
    verified_at TIMESTAMPTZ
);

CREATE INDEX idx_issue_work_attempts_work_item_id ON issue_work_attempts(work_item_id);
CREATE INDEX idx_issue_work_attempts_status ON issue_work_attempts(status);
CREATE INDEX idx_issue_work_attempts_verification_crawl_id ON issue_work_attempts(verification_crawl_id);

CREATE TABLE issue_work_attempt_contributors (
    attempt_id UUID NOT NULL REFERENCES issue_work_attempts(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    marked_done_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (attempt_id, user_id)
);

CREATE INDEX idx_issue_work_attempt_contributors_user_id ON issue_work_attempt_contributors(user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS issue_work_attempt_contributors;
DROP TABLE IF EXISTS issue_work_attempts;
DROP TABLE IF EXISTS issue_work_items;
-- +goose StatementEnd
