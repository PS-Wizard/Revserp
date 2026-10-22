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
RETURNING id, job_type, project_id, audit_id, local_run_id, location_id, competitor_id, status, error_message, started_at, completed_at, created_at, updated_at;

-- name: EnqueueAIWorkerJob :one
INSERT INTO ai_worker_jobs (job_type, project_id, audit_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING id, job_type, project_id, audit_id, status, error_message, started_at, completed_at, created_at, updated_at;

-- name: EnqueueCompetitorAIWorkerJob :one
INSERT INTO ai_worker_jobs (job_type, project_id, competitor_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING id, job_type, project_id, competitor_id, status, error_message, started_at, completed_at, created_at, updated_at;

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
-- project or competitor setup step in the same statement, so a crashed worker
-- cannot leave a setup active forever. Non-setup jobs (maps_visibility,
-- unknown) keep the plain reclaim behavior.
WITH stale AS (
    UPDATE ai_worker_jobs
    SET status = 'failed', error_message = 'reclaimed: worker restarted', completed_at = NOW(), updated_at = NOW()
    WHERE ai_worker_jobs.status = 'running'
      AND ai_worker_jobs.started_at < $1
    RETURNING job_type, project_id, audit_id, location_id, competitor_id
),
reclaimed_location_audits AS (
    UPDATE ai_audits AS a
    SET status = 'failed',
        error_message = 'reclaimed: worker restarted during location audit',
        completed_at = NOW(),
        updated_at = NOW()
    FROM stale
    WHERE stale.job_type = 'visibility_run'
      AND a.id = stale.audit_id
      AND a.project_id = stale.project_id
      AND a.location_id IS NOT NULL
      AND a.status IN ('queued', 'running')
    RETURNING a.id
),
reclaimed_project_setup AS (
UPDATE project_setup AS ps
SET status = 'failed',
    error = 'reclaimed: worker restarted during setup; retry',
    failed_step = CASE stale.job_type
        WHEN 'business_profile_bootstrap' THEN 'profile_generation'
        WHEN 'keyword_analysis' THEN 'keyword_analysis'
        WHEN 'prompt_generation' THEN 'prompt_generation'
        WHEN 'visibility_run' THEN 'visibility'
    END,
    updated_at = now(),
    completed_at = now()
FROM stale
WHERE ps.project_id = stale.project_id
  -- A location prompt job must never fail the parent project setup, and
  -- competitor jobs fail only the competitor setup below.
  AND stale.location_id IS NULL
  AND stale.competitor_id IS NULL
  AND stale.job_type IN ('business_profile_bootstrap', 'keyword_analysis', 'prompt_generation', 'visibility_run')
  AND ps.status = CASE stale.job_type
        WHEN 'business_profile_bootstrap' THEN 'profile_generation'
        WHEN 'keyword_analysis' THEN 'keyword_analysis'
        WHEN 'prompt_generation' THEN 'prompt_generation'
        WHEN 'visibility_run' THEN 'visibility'
    END
  AND (stale.job_type <> 'visibility_run'
       OR NOT EXISTS (SELECT 1 FROM ai_audits a
                      WHERE a.id = stale.audit_id AND a.location_id IS NOT NULL))
RETURNING ps.id
)
UPDATE competitor_setup AS cs
SET status = 'failed',
    error = 'reclaimed: worker restarted during competitor setup; retry',
    failed_step = CASE stale.job_type
        WHEN 'competitor_keyword_generation' THEN 'keyword_generation'
        WHEN 'competitor_keyword_analysis' THEN 'keyword_analysis'
    END,
    keyword_generation_status = CASE stale.job_type
        WHEN 'competitor_keyword_generation' THEN 'failed'
        ELSE cs.keyword_generation_status
    END,
    keyword_analysis_status = CASE stale.job_type
        WHEN 'competitor_keyword_analysis' THEN 'failed'
        ELSE cs.keyword_analysis_status
    END,
    updated_at = now(),
    completed_at = now()
FROM stale
WHERE cs.project_id = stale.project_id
  AND cs.competitor_id = stale.competitor_id
  AND stale.competitor_id IS NOT NULL
  AND stale.job_type IN ('competitor_keyword_generation', 'competitor_keyword_analysis')
  AND cs.status = 'running'
  AND CASE stale.job_type
        WHEN 'competitor_keyword_generation' THEN cs.keyword_generation_status
        WHEN 'competitor_keyword_analysis' THEN cs.keyword_analysis_status
    END = 'running';

-- name: GetLatestPromptGenerationJobByProject :one
SELECT id, job_type, project_id, audit_id, status, error_message, started_at, completed_at, created_at, updated_at
FROM ai_worker_jobs
WHERE project_id = $1 AND job_type = 'prompt_generation' AND location_id IS NULL
ORDER BY created_at DESC
LIMIT 1;
