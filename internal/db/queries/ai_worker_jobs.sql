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
-- Orphan recovery: any ai_worker_jobs row stuck in 'running' past the cutoff is
-- marked failed. Setup-linked job types additionally fail the matching active
-- setup step in the same statement, so a crashed worker cannot leave a setup
-- active forever. Non-setup jobs (maps_visibility, unknown) keep the plain
-- reclaim behavior.
WITH stale AS (
    UPDATE ai_worker_jobs
    SET status = 'failed', error_message = 'reclaimed: worker restarted', completed_at = NOW(), updated_at = NOW()
    WHERE status = 'running'
      AND started_at < $1
    RETURNING job_type, project_id
)
UPDATE project_setup AS ps
SET status = 'failed',
    error = 'reclaimed: worker restarted during setup; retry',
    failed_step = CASE stale.job_type
        WHEN 'business_profile_bootstrap' THEN 'profile_generation'
        WHEN 'prompt_generation' THEN 'prompt_generation'
        WHEN 'visibility_run' THEN 'visibility'
    END,
    updated_at = now(),
    completed_at = now()
FROM stale
WHERE ps.project_id = stale.project_id
  AND ps.status = CASE stale.job_type
        WHEN 'business_profile_bootstrap' THEN 'profile_generation'
        WHEN 'prompt_generation' THEN 'prompt_generation'
        WHEN 'visibility_run' THEN 'visibility'
    END;

-- name: GetLatestPromptGenerationJobByProject :one
SELECT id, job_type, project_id, audit_id, status, error_message, started_at, completed_at, created_at, updated_at
FROM ai_worker_jobs
WHERE project_id = $1 AND job_type = 'prompt_generation'
ORDER BY created_at DESC
LIMIT 1;
