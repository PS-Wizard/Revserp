-- name: UpsertProjectKeywordAnalysis :one
INSERT INTO project_keyword_analysis (
    project_id, keyword, normalized_keyword, kind,
    rank, traffic, volume, kd, cpc, results_count
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (project_id, normalized_keyword) DO UPDATE SET
    keyword = EXCLUDED.keyword,
    kind = EXCLUDED.kind,
    rank = EXCLUDED.rank,
    traffic = EXCLUDED.traffic,
    volume = EXCLUDED.volume,
    kd = EXCLUDED.kd,
    cpc = EXCLUDED.cpc,
    results_count = EXCLUDED.results_count,
    updated_at = now()
RETURNING id, project_id, keyword, normalized_keyword, kind, rank, traffic, volume, kd, cpc, results_count, created_at, updated_at;

-- name: ListProjectKeywordAnalysisByProject :many
SELECT id, project_id, keyword, normalized_keyword, kind, rank, traffic, volume, kd, cpc, results_count, created_at, updated_at
FROM project_keyword_analysis
WHERE project_id = $1
ORDER BY kind ASC, keyword ASC;

-- name: UpsertCompetitorKeywordAnalysis :one
INSERT INTO competitor_keyword_analysis (
    competitor_id, keyword, normalized_keyword, kind,
    rank, traffic, volume, kd, cpc, results_count
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (competitor_id, normalized_keyword) DO UPDATE SET
    keyword = EXCLUDED.keyword,
    kind = EXCLUDED.kind,
    rank = EXCLUDED.rank,
    traffic = EXCLUDED.traffic,
    volume = EXCLUDED.volume,
    kd = EXCLUDED.kd,
    cpc = EXCLUDED.cpc,
    results_count = EXCLUDED.results_count,
    updated_at = now()
RETURNING id, competitor_id, keyword, normalized_keyword, kind, rank, traffic, volume, kd, cpc, results_count, created_at, updated_at;

-- name: ListCompetitorKeywordAnalysisByCompetitor :many
SELECT id, competitor_id, keyword, normalized_keyword, kind, rank, traffic, volume, kd, cpc, results_count, created_at, updated_at
FROM competitor_keyword_analysis
WHERE competitor_id = $1
ORDER BY kind ASC, keyword ASC;

-- name: DeleteCompetitorKeywordAnalysisByCompetitor :exec
DELETE FROM competitor_keyword_analysis WHERE competitor_id = $1;

-- name: UpsertLocationKeywordAnalysis :one
INSERT INTO location_keyword_analysis (
    location_id, keyword, normalized_keyword, kind,
    rank, traffic, volume, kd, cpc, results_count
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (location_id, normalized_keyword) DO UPDATE SET
    keyword = EXCLUDED.keyword,
    kind = EXCLUDED.kind,
    rank = EXCLUDED.rank,
    traffic = EXCLUDED.traffic,
    volume = EXCLUDED.volume,
    kd = EXCLUDED.kd,
    cpc = EXCLUDED.cpc,
    results_count = EXCLUDED.results_count,
    updated_at = now()
RETURNING id, location_id, keyword, normalized_keyword, kind, rank, traffic, volume, kd, cpc, results_count, created_at, updated_at;

-- name: ListLocationKeywordAnalysisByLocation :many
SELECT id, location_id, keyword, normalized_keyword, kind, rank, traffic, volume, kd, cpc, results_count, created_at, updated_at
FROM location_keyword_analysis
WHERE location_id = $1
ORDER BY kind ASC, keyword ASC;
