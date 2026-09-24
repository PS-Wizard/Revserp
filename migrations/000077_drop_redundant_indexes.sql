-- +goose Up
-- +goose StatementBegin
-- Drop ten indexes that are either redundant by construction or have never been
-- used.
--
-- Evidence: pg_stat_user_indexes.idx_scan is a lifetime counter in this database
-- because pg_stat_database.stats_reset has never been set. A zero therefore
-- means "not used once since this database was created", not "not used lately".
--
-- Every index write also costs a WAL record, so these are paid for on every
-- crawl: 137,956 link rows, 1,928 page rows and about 14,820 issue rows per
-- crawl. Dropping these ten removes roughly 220,000 index writes per crawl.
--
-- Group A, redundant by construction. Postgres can serve any query that filters
-- on an index's leading column from a composite index, so each of these is
-- already covered by a neighbour and the scan counts do not matter:
--
--   idx_crawl_links_crawl_id          covered by (crawl_id, is_internal),
--                                     (crawl_id, target_url_key), and the unique
--                                     (crawl_id, source_url, target_url, anchor)
--   idx_crawl_pages_crawl_id          covered by (crawl_id, url),
--                                     (crawl_id, status_code), (crawl_id, url_key)
--   idx_crawl_pages_crawl_id_url      exact duplicate of the UNIQUE constraint
--                                     crawl_pages_crawl_id_url_key
--   idx_crawl_issues_crawl_id         covered by (crawl_id, severity) and
--                                     (crawl_id, created_at DESC)
--   idx_crawl_issues_crawl_id_pillar  covered by
--                                     (crawl_id, pillar, bucket, issue_type)
--   idx_crawls_project_id             covered by (project_id, created_at DESC)
--                                     and (project_id, completed_at DESC)
--
-- Group B, zero lifetime scans:
--
--   idx_crawl_pages_crawl_id_url_key  added in 000076 and never used
--   idx_crawl_issues_crawl_id_created_at
--   idx_crawl_issues_issue_group_id
--   idx_crawls_project_status         partial, WHERE source = 'auto'
--
-- Deliberately kept, for the opposite reason. These have low but nonzero scans
-- and no neighbour covers their filter shape, so the saving is a few MB against
-- a slow query nobody would notice for weeks:
--
--   idx_crawl_pages_crawl_id_created_at  (3 scans)
--   idx_crawl_issues_crawl_id_bucket     (1 scan)
--
-- lock_timeout is set here rather than relied on from the connection: the
-- migrate binary opens its own database/sql connection and does not go through
-- internal/db, which is where the app's lock_timeout is applied. DROP INDEX
-- needs ACCESS EXCLUSIVE, and without a timeout it could wait forever behind a
-- running crawl. On a Coolify deploy the app services wait on
-- migrate completing, so nothing is querying these tables; this covers the other
-- cases, such as running the migration by hand while the app is up.
SET lock_timeout = '5s';

DROP INDEX IF EXISTS idx_crawl_links_crawl_id;
DROP INDEX IF EXISTS idx_crawl_pages_crawl_id;
DROP INDEX IF EXISTS idx_crawl_pages_crawl_id_url;
DROP INDEX IF EXISTS idx_crawl_pages_crawl_id_url_key;
DROP INDEX IF EXISTS idx_crawl_issues_crawl_id;
DROP INDEX IF EXISTS idx_crawl_issues_crawl_id_pillar;
DROP INDEX IF EXISTS idx_crawl_issues_crawl_id_created_at;
DROP INDEX IF EXISTS idx_crawl_issues_issue_group_id;
DROP INDEX IF EXISTS idx_crawls_project_id;
DROP INDEX IF EXISTS idx_crawls_project_status;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Recreate exactly what the Up dropped, from the definitions captured before the
-- change. No archaeology needed if one of these turns out to be wanted.
CREATE INDEX idx_crawl_links_crawl_id ON crawl_links (crawl_id);
CREATE INDEX idx_crawl_pages_crawl_id ON crawl_pages (crawl_id);
CREATE INDEX idx_crawl_pages_crawl_id_url ON crawl_pages (crawl_id, url);
CREATE INDEX idx_crawl_pages_crawl_id_url_key ON crawl_pages (crawl_id, url_key);
CREATE INDEX idx_crawl_issues_crawl_id ON crawl_issues (crawl_id);
CREATE INDEX idx_crawl_issues_crawl_id_pillar ON crawl_issues (crawl_id, pillar);
CREATE INDEX idx_crawl_issues_crawl_id_created_at ON crawl_issues (crawl_id, created_at DESC);
CREATE INDEX idx_crawl_issues_issue_group_id ON crawl_issues (issue_group_id);
CREATE INDEX idx_crawls_project_id ON crawls (project_id);
CREATE INDEX idx_crawls_project_status ON crawls (project_id, status) WHERE source = 'auto';
-- +goose StatementEnd
