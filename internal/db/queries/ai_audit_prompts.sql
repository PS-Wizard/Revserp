-- name: ListAIAuditPromptsByAuditID :many
SELECT
    id,
    audit_id,
    prompt_text,
    prompt_source,
    display_order,
    created_at
FROM ai_audit_prompts
WHERE audit_id = $1
ORDER BY display_order ASC, created_at ASC;
