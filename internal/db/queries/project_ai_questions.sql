-- name: UpsertProjectAIQuestions :one
INSERT INTO project_ai_questions (project_id, questions, generation_model)
VALUES ($1, $2, $3)
ON CONFLICT (project_id) DO UPDATE SET
    questions = EXCLUDED.questions,
    generation_model = EXCLUDED.generation_model,
    generated_at = NOW()
RETURNING id, project_id, questions, generation_model, generated_at;

-- name: GetProjectAIQuestions :one
SELECT id, project_id, questions, generation_model, generated_at
FROM project_ai_questions
WHERE project_id = $1;
