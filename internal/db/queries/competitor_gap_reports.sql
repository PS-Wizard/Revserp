-- name: UpsertCompetitorGapReport :exec
INSERT INTO competitor_gap_reports (
    parent_crawl_id,
    competitor_crawl_id,
    version,
    report_json
) VALUES (
    $1,
    $2,
    $3,
    $4
)
ON CONFLICT (competitor_crawl_id) DO UPDATE SET
    report_json = EXCLUDED.report_json,
    version = EXCLUDED.version,
    updated_at = now(),
    parent_crawl_id = EXCLUDED.parent_crawl_id;

-- name: GetCompetitorGapReportByCompetitorCrawlID :one
SELECT
    id,
    parent_crawl_id,
    competitor_crawl_id,
    version,
    report_json,
    created_at,
    updated_at
FROM competitor_gap_reports
WHERE competitor_crawl_id = $1
LIMIT 1;
