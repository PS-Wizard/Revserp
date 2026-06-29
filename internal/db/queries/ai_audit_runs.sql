-- name: ListAIAuditRunsByAuditID :many
SELECT
    id,
    audit_id,
    question_text,
    display_order,
    model_name,
    status,
    raw_response,
    parsed_response_json,
    mentioned_target,
    target_rank,
    visibility_score,
    error_message,
    started_at,
    completed_at,
    created_at,
    updated_at
FROM ai_audit_runs
WHERE audit_id = $1
ORDER BY display_order ASC, model_name ASC;
