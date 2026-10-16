-- name: ListUserAnnouncementSeen :many
SELECT announcement_id, seen_at
FROM user_announcement_seen
WHERE user_id = $1
ORDER BY seen_at DESC, announcement_id ASC;

-- name: MarkUserAnnouncementSeen :exec
INSERT INTO user_announcement_seen (user_id, announcement_id)
VALUES ($1, $2)
ON CONFLICT (user_id, announcement_id) DO NOTHING;
