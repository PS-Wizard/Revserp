-- name: GetAIPromptConfig :one
SELECT id, internal_system_prompt, external_system_prompt, question_generation_prompt, updated_by_user_id, updated_at
FROM ai_prompt_configs
WHERE id = 1;

-- name: UpsertAIPromptConfig :one
INSERT INTO ai_prompt_configs (
    id,
    internal_system_prompt,
    external_system_prompt,
    question_generation_prompt,
    updated_by_user_id
) VALUES (
    1,
    $1,
    $2,
    $3,
    $4
)
ON CONFLICT (id) DO UPDATE SET
    internal_system_prompt = EXCLUDED.internal_system_prompt,
    external_system_prompt = EXCLUDED.external_system_prompt,
    question_generation_prompt = EXCLUDED.question_generation_prompt,
    updated_by_user_id = EXCLUDED.updated_by_user_id,
    updated_at = NOW()
RETURNING id, internal_system_prompt, external_system_prompt, question_generation_prompt, updated_by_user_id, updated_at;

-- name: ResetAIPromptConfig :exec
DELETE FROM ai_prompt_configs WHERE id = 1;
