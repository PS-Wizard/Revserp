-- +goose Up
-- +goose StatementBegin

ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs
    ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run', 'maps_visibility'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE ai_worker_jobs DROP CONSTRAINT ai_worker_jobs_job_type_check;
ALTER TABLE ai_worker_jobs
    ADD CONSTRAINT ai_worker_jobs_job_type_check
    CHECK (job_type IN ('prompt_generation', 'visibility_run'));

-- +goose StatementEnd
