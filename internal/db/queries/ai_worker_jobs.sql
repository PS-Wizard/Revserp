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
RETURNING id, job_type, project_id, audit_id, status, error_message, started_at, completed_at, created_at, updated_at;

-- name: EnqueueAIWorkerJob :one
INSERT INTO ai_worker_jobs (job_type, project_id, audit_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING id, job_type, project_id, audit_id, status, error_message, started_at, completed_at, created_at, updated_at;

-- name: MarkAIWorkerJobCompleted :exec
UPDATE ai_worker_jobs
SET status = 'completed', completed_at = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: MarkAIWorkerJobFailed :exec
UPDATE ai_worker_jobs
SET status = 'failed', error_message = $2, completed_at = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: ReclaimStaleRunningAIWorkerJobs :exec
UPDATE ai_worker_jobs
SET status = 'failed', error_message = 'reclaimed: worker restarted', completed_at = NOW(), updated_at = NOW()
WHERE status = 'running'
  AND started_at < $1;
