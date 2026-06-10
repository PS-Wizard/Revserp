-- name: ListAIAuditRunsByAuditID :many
SELECT
    aar.id,
    aar.audit_id,
    aar.prompt_id,
    aar.model_name,
    aar.status,
    aar.raw_response,
    aar.parsed_response_json,
    aar.mentioned_target,
    aar.target_rank,
    aar.visibility_score,
    aar.error_message,
    aar.started_at,
    aar.completed_at,
    aar.created_at,
    aar.updated_at
FROM ai_audit_runs AS aar
INNER JOIN ai_audit_prompts AS aap ON aap.id = aar.prompt_id
WHERE aar.audit_id = $1
ORDER BY aap.display_order ASC, aar.model_name ASC, aar.created_at ASC;
