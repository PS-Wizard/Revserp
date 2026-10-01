-- name: ListProjectKeywordsByProject :many
SELECT id, project_id, keyword, normalized_keyword, kind, source, created_at, updated_at
FROM project_keywords
WHERE project_id = $1
ORDER BY created_at ASC, id ASC;

-- name: CountProjectKeywordsByProject :many
SELECT source, kind, COUNT(*) AS keyword_count
FROM project_keywords
WHERE project_id = $1
GROUP BY source, kind;

-- name: GetProjectKeywordByProjectSourceNormalized :one
SELECT id, project_id, keyword, normalized_keyword, kind, source, created_at, updated_at
FROM project_keywords
WHERE project_id = $1 AND source = $2 AND normalized_keyword = $3
LIMIT 1;

-- name: InsertProjectKeyword :one
INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, keyword, normalized_keyword, kind, source, created_at, updated_at;

-- name: DeleteRevserpProjectKeywordsByProject :exec
DELETE FROM project_keywords WHERE project_id = $1 AND source = 'revserp';

-- name: DeleteUserProjectKeyword :one
DELETE FROM project_keywords WHERE id = $1 AND project_id = $2 AND source = 'user'
RETURNING id;
