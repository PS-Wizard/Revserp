-- name: GetAIPromptConfig :one
SELECT id, context_prompt, guidelines_prompt, other_notes_prompt, updated_by_user_id, updated_at
FROM ai_prompt_configs
WHERE id = 1;

-- name: UpsertAIPromptConfig :one
INSERT INTO ai_prompt_configs (
    id,
    context_prompt,
    guidelines_prompt,
    other_notes_prompt,
    updated_by_user_id
) VALUES (
    1,
    $1,
    $2,
    $3,
    $4
)
ON CONFLICT (id) DO UPDATE SET
    context_prompt = EXCLUDED.context_prompt,
    guidelines_prompt = EXCLUDED.guidelines_prompt,
    other_notes_prompt = EXCLUDED.other_notes_prompt,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = NOW()
RETURNING id, context_prompt, guidelines_prompt, other_notes_prompt, updated_by_user_id, updated_at;

-- name: ResetAIPromptConfig :exec
DELETE FROM ai_prompt_configs WHERE id = 1;
