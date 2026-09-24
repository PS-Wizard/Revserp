-- +goose Up
-- +goose StatementBegin
-- Tune autovacuum for the two tables with the most churn.
--
-- The default autovacuum_vacuum_scale_factor of 0.2 means a table must reach 20
-- percent dead tuples before autovacuum will collect it. On crawl_links, which
-- is 3.5M rows in production, that is a threshold of about 710,000 dead tuples.
-- One crawl's link status resolution alone touches roughly 100,000 rows and
-- leaves a dead tuple behind for each one, so the table runs at 5 percent dead
-- today and is allowed to climb to 20 before anything happens.
--
-- 0.05 puts the threshold at about 178,000, so it fires roughly once per crawl.
-- A smaller factor would be worse rather than better: 0.02 would put the
-- threshold below what a single crawl produces, so autovacuum would run
-- continuously through every crawl and compete for I/O on a shared box.
--
-- The analyze factor matters separately, and there is a measurement behind it.
-- Running ANALYZE by hand took one query from 154ms to 62ms because the planner
-- was working from stale statistics. The default threshold on a 3.5M row table
-- is about 355,000 changes. 0.01 brings that down to about 35,000.
--
-- crawl_issues is deliberately not included. It sits at 0.03 percent dead
-- because each crawl replaces its issues rather than updating them in place.
ALTER TABLE crawl_links SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.01
);

ALTER TABLE crawl_pages SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.01
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE crawl_links RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor
);

ALTER TABLE crawl_pages RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor
);
-- +goose StatementEnd
