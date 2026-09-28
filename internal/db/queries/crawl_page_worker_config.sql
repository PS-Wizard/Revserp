-- name: GetCrawlPageWorkerConfig :one
SELECT id, worker_count, updated_by_user_id, updated_at
FROM crawl_page_worker_config
WHERE id = TRUE;

-- name: UpsertCrawlPageWorkerConfig :one
INSERT INTO crawl_page_worker_config (
    id,
    worker_count,
    updated_by_user_id
) VALUES (
    TRUE,
    $1,
    $2
)
ON CONFLICT (id) DO UPDATE SET
    worker_count = EXCLUDED.worker_count,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = NOW()
RETURNING id, worker_count, updated_by_user_id, updated_at;

-- name: ResetCrawlPageWorkerConfig :exec
DELETE FROM crawl_page_worker_config WHERE id = TRUE;
