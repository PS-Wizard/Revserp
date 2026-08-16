-- name: InsertAIToolCall :one
INSERT INTO ai_tool_calls (turn_id, seq, call_id, name, args, status)
VALUES (
    sqlc.arg(turn_id),
    sqlc.arg(seq),
    sqlc.arg(call_id),
    sqlc.arg(name),
    sqlc.arg(args)::jsonb,
    sqlc.arg(status)
)
RETURNING id;

-- name: CompleteAIToolCall :exec
UPDATE ai_tool_calls
SET status = sqlc.arg(status),
    result_content = sqlc.arg(result_content),
    summary = sqlc.arg(summary),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ListAIToolCallsForTurn :many
SELECT call_id, name, args, status, summary, seq, created_at
FROM ai_tool_calls
WHERE turn_id = sqlc.arg(turn_id)
ORDER BY seq ASC;
