-- name: ListCompetitorKeywordSelectionsByProject :many
SELECT id, project_id, competitor_id, keyword, normalized_keyword, kind, created_at, updated_at
FROM project_competitor_keyword_selections
WHERE project_id = $1
ORDER BY competitor_id ASC, keyword ASC;

-- name: UpsertCompetitorKeywordSelection :one
INSERT INTO project_competitor_keyword_selections (project_id, competitor_id, keyword, normalized_keyword, kind)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (competitor_id, normalized_keyword) DO UPDATE SET
    project_id = EXCLUDED.project_id,
    keyword = EXCLUDED.keyword,
    kind = EXCLUDED.kind,
    updated_at = now()
RETURNING id, project_id, competitor_id, keyword, normalized_keyword, kind, created_at, updated_at;

-- name: DeleteCompetitorKeywordSelection :exec
DELETE FROM project_competitor_keyword_selections
WHERE project_id = $1 AND competitor_id = $2 AND normalized_keyword = $3;
