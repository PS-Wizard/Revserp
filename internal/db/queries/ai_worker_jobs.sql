-- name: ClaimNextPendingAIWorkerJob :one
UPDATE ai_worker_jobs
SET status = 'running', started_at = NOW(), updated_at = NOW()
WHERE id = (
    SELECT id FROM ai_worker_jobs
    WHERE status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING id, job_type, project_id, status, error_message, started_at, completed_at, created_at, updated_at;

-- name: EnqueueAIWorkerJob :one
INSERT INTO ai_worker_jobs (job_type, project_id)
VALUES ($1, $2)
RETURNING id, job_type, project_id, status, error_message, started_at, completed_at, created_at, updated_at;

-- name: MarkAIWorkerJobCompleted :exec
UPDATE ai_worker_jobs
SET status = 'completed', completed_at = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: MarkAIWorkerJobFailed :exec
UPDATE ai_worker_jobs
SET status = 'failed', error_message = $2, completed_at = NOW(), updated_at = NOW()
WHERE id = $1;
