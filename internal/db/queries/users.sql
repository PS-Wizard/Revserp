-- name: GetUserByAuthSubject :one
SELECT id, auth_provider, auth_subject, email, name, created_at
FROM users
WHERE auth_provider = $1 AND auth_subject = $2
LIMIT 1;

-- name: CreateUser :one
INSERT INTO users (
    auth_provider,
    auth_subject,
    email,
    name
) VALUES (
    $1,
    $2,
    $3,
    $4
)
RETURNING id, auth_provider, auth_subject, email, name, created_at;
